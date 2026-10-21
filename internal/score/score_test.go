package score

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tragent-io/acurl/pkg/types"
)

var update = flag.Bool("update", false, "rewrite golden files")

var product = &types.Product{Source: "json-ld", Price: "98", Currency: "USD", Availability: "InStock"}

func res(name, cat, class string) types.FetchResult {
	return types.FetchResult{Profile: name, Category: cat, Classification: class, JS: &types.JSInfo{TextLength: 900}, Product: product}
}

func baselineResult() types.FetchResult {
	r := res("Chrome", types.CategoryBrowser, "allowed")
	r.Browser = true
	return r
}

func TestComputeGolden(t *testing.T) {
	tests := map[string]types.ComparisonResult{
		"perfect": {Results: []types.FetchResult{
			baselineResult(),
			res("ChatGPT-User", types.CategoryAIAssistant, "allowed"),
			res("OAI-SearchBot", types.CategoryAISearch, "allowed"),
			res("GPTBot", types.CategoryAITraining, "robots-disallowed"),
		}},
		"blocked-assistant": {Results: []types.FetchResult{
			baselineResult(),
			res("ChatGPT-User", types.CategoryAIAssistant, "challenged"),
			res("Claude-User", types.CategoryAIAssistant, "allowed"),
			res("OAI-SearchBot", types.CategoryAISearch, "blocked"),
		}},
		"spa-without-product": {Results: []types.FetchResult{
			{Profile: "Chrome", Browser: true, Category: types.CategoryBrowser, Classification: "allowed", JS: &types.JSInfo{ClientRendered: true, Reason: "empty app root element"}},
			{Profile: "ChatGPT-User", Category: types.CategoryAIAssistant, Classification: "cloaked"},
		}},
	}
	for name, rep := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := json.MarshalIndent(Compute(&rep), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", name+".golden.json")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, append(got, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/score -update)", err)
			}
			if string(want) != string(got)+"\n" {
				t.Errorf("score mismatch for %s:\n%s\nwant:\n%s", name, got, want)
			}
		})
	}
}

func TestGrades(t *testing.T) {
	for v, want := range map[int]string{100: "A", 90: "A", 85: "B", 70: "C", 55: "D", 10: "F"} {
		if g := grade(v); g != want {
			t.Errorf("grade(%d) = %s, want %s", v, g, want)
		}
	}
}

func TestEveryFailedCheckHasRecommendation(t *testing.T) {
	rep := types.ComparisonResult{Results: []types.FetchResult{
		{Profile: "Chrome", Browser: true, Category: types.CategoryBrowser, Classification: "allowed", JS: &types.JSInfo{ClientRendered: true}, Product: &types.Product{Source: "opengraph"}},
		{Profile: "A", Category: types.CategoryAIAssistant, Classification: "challenged"},
		{Profile: "S", Category: types.CategoryAISearch, Classification: "cloaked"},
		{Profile: "T", Category: types.CategoryAITraining, Classification: "allowed", ProductIssue: "price"},
	}}
	for _, c := range Compute(&rep).Checks {
		if c.Applies && !c.Passed && c.Recommendation == "" {
			t.Errorf("check %s failed without a recommendation", c.ID)
		}
	}
}

func TestComputeSkipsWhenBaselineBlocked(t *testing.T) {
	rep := types.ComparisonResult{Results: []types.FetchResult{
		{Profile: "Chrome", Browser: true, Category: types.CategoryBrowser, Classification: "challenged"},
		{Profile: "GPTBot", Category: types.CategoryAITraining, Classification: "allowed"},
	}}
	s := Compute(&rep)
	if s.Grade != "-" || s.Value != 0 || s.Note == "" || len(s.Checks) != 0 {
		t.Errorf("Compute = %+v", s)
	}
}

func findCheck(t *testing.T, s *types.Score, id string) types.ScoreCheck {
	t.Helper()
	for _, c := range s.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %s", id)
	return types.ScoreCheck{}
}

func plainPage(url string, product *types.Product, directives ...string) *types.ComparisonResult {
	js := &types.JSInfo{TextLength: 900}
	return &types.ComparisonResult{URL: url, Results: []types.FetchResult{
		{Profile: "Chrome", Browser: true, Category: types.CategoryBrowser, Classification: "allowed", JS: js, Product: product, Directives: directives, ContentType: "text/html; charset=utf-8"},
		{Profile: "ChatGPT-User", Category: types.CategoryAIAssistant, Classification: "allowed", JS: js, Product: product, Directives: directives},
	}}
}

func TestProductPageWithoutSchemaLosesPoints(t *testing.T) {
	// B3: a product URL with no Product schema must not score 100.
	s := Compute(plainPage("https://www.suunto.com/Products/sports-watches/suunto-race-3s/", nil))
	if c := findCheck(t, s, "structured-data"); !c.Applies || c.Passed || s.Value == 100 {
		t.Errorf("structured-data = %+v, score %d", c, s.Value)
	}
	// Not a product page: the check is skipped.
	s = Compute(plainPage("https://shop.example/about-us", nil))
	if c := findCheck(t, s, "structured-data"); c.Applies || s.Value != 100 {
		t.Errorf("about page: %+v, score %d", c, s.Value)
	}
	// --page overrides the URL heuristic both ways.
	rep := plainPage("https://shop.example/about-us", nil)
	rep.Page = types.PageProduct
	if c := findCheck(t, Compute(rep), "structured-data"); !c.Applies {
		t.Error("--page product should expect product data")
	}
	rep = plainPage("https://shop.example/products/gift-card-info", nil)
	rep.Page = types.PageOther
	if c := findCheck(t, Compute(rep), "structured-data"); c.Applies {
		t.Error("--page other should not expect product data")
	}
}

func TestNoindexLosesPoints(t *testing.T) {
	// B4: noindex,nofollow must cost points.
	s := Compute(plainPage("https://shop.example/", nil, "noindex", "nofollow"))
	if c := findCheck(t, s, "indexable"); c.Passed || c.Recommendation == "" || s.Value == 100 {
		t.Errorf("indexable = %+v, score %d", c, s.Value)
	}
}

func TestIndexableIgnoresNoindexFromUnavailableAgents(t *testing.T) {
	for _, classification := range []string{"challenged", "blocked"} {
		t.Run(classification, func(t *testing.T) {
			agent := res("OAI-SearchBot", types.CategoryAISearch, classification)
			agent.Directives = []string{"noindex", "nofollow"}
			rep := &types.ComparisonResult{Results: []types.FetchResult{baselineResult(), agent}}

			if c := findCheck(t, Compute(rep), "indexable"); !c.Passed || c.Detail != "no noindex directives" {
				t.Errorf("indexable = %+v", c)
			}
		})
	}
}

func TestIndexableCountsOnlyVisibleAgentRows(t *testing.T) {
	allowed := res("OAI-SearchBot", types.CategoryAISearch, "allowed")
	allowed.Directives = []string{"noindex"}
	rep := &types.ComparisonResult{Results: []types.FetchResult{
		baselineResult(),
		allowed,
		res("Claude-SearchBot", types.CategoryAISearch, "cloaked"),
		res("PerplexityBot", types.CategoryAISearch, "challenged"),
		res("Google-Extended", types.CategoryAITraining, "blocked"),
	}}

	if c := findCheck(t, Compute(rep), "indexable"); c.Points != 7.5 || c.Detail != "noindex for: OAI-SearchBot" {
		t.Errorf("indexable = %+v", c)
	}
}

func TestUnscorableReasons(t *testing.T) {
	base := func(r types.FetchResult) *types.ComparisonResult {
		r.Profile, r.Browser, r.Category = "Chrome", true, types.CategoryBrowser
		return &types.ComparisonResult{Results: []types.FetchResult{r}}
	}
	tests := []struct {
		name string
		rep  *types.ComparisonResult
		want string
	}{
		{"image", base(types.FetchResult{Classification: "allowed", StatusCode: 200, ContentType: "image/png"}), "image/png, not HTML"},
		{"dns", base(types.FetchResult{Classification: "error", Error: `Get "https://x.invalid": dial tcp: lookup x.invalid: no such host`}), "DNS lookup failed"},
		{"private", base(types.FetchResult{Classification: "error", Error: "connection to a private or internal address blocked: the host resolved to 198.18.0.1"}), "private or internal address"},
		{"500", base(types.FetchResult{Classification: "blocked", StatusCode: 500}), "HTTP 500"},
		{"challenge", base(types.FetchResult{Classification: "challenged", StatusCode: 403, BotChallenge: "Akamai"}), "challenged by Akamai"},
	}
	for _, tt := range tests {
		s := Compute(tt.rep)
		if s.Grade != "-" || !strings.Contains(s.Note, tt.want) {
			t.Errorf("%s: grade %q note %q, want it to mention %q", tt.name, s.Grade, s.Note, tt.want)
		}
		if strings.Contains(s.Note, "was error") {
			t.Errorf("%s: ungrammatical note %q", tt.name, s.Note)
		}
	}
}
