package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tragent-io/acurl/internal/output"
	"github.com/tragent-io/acurl/pkg/types"
)

// shop blocks ChatGPT-User and disallows PerplexityBot in robots.txt.
func shop(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: PerplexityBot\nDisallow: /\n"))
			return
		}
		if strings.Contains(r.UserAgent(), "ChatGPT-User") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<html><title>Forbidden</title></html>"))
			return
		}
		_, _ = w.Write([]byte("<html><head><title>Shop</title></head><body><p>Red shoes in every size, shipped free worldwide.</p></body></html>"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Execute(context.Background(), BuildInfo{Version: "test"}, args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestVersionCommand(t *testing.T) {
	info := BuildInfo{Version: "1.2.3", Commit: "abc1234", Date: "2026-01-02T03:04:05Z"}
	want := "acurl version 1.2.3 (commit abc1234, built 2026-01-02T03:04:05Z)\n"

	var stdout, stderr bytes.Buffer
	if code := Execute(context.Background(), info, []string{"version"}, &stdout, &stderr); code != output.ExitOK {
		t.Fatalf("acurl version: code=%d stderr=%q", code, stderr.String())
	}
	if stdout.String() != want {
		t.Errorf("acurl version output = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("acurl version stderr = %q", stderr.String())
	}

	var flagOut, flagErr bytes.Buffer
	if code := Execute(context.Background(), info, []string{"--version"}, &flagOut, &flagErr); code != output.ExitOK {
		t.Fatalf("acurl --version: code=%d stderr=%q", code, flagErr.String())
	}
	if flagOut.String() != stdout.String() {
		t.Errorf("acurl --version output = %q, acurl version output = %q", flagOut.String(), stdout.String())
	}

	t.Chdir(t.TempDir())
	if err := os.WriteFile(".acurl.yaml", []byte("not: [valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	var cfgOut, cfgErr bytes.Buffer
	if code := Execute(context.Background(), info, []string{"version"}, &cfgOut, &cfgErr); code != output.ExitOK {
		t.Fatalf("acurl version with a broken config: code=%d stderr=%q", code, cfgErr.String())
	}
	if cfgOut.String() != want {
		t.Errorf("acurl version output with a broken config = %q, want %q", cfgOut.String(), want)
	}
}

func TestCheckJSONReport(t *testing.T) {
	srv := shop(t)
	code, stdout, stderr := run(t, srv.URL, "-f", "json", "-a", "Chrome (Browser),ChatGPT-User,PerplexityBot,Googlebot")

	if code != output.ExitBlocked {
		t.Errorf("exit code = %d, want %d (stderr: %s)", code, output.ExitBlocked, stderr)
	}
	var rep types.ComparisonResult
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	got := map[string]string{}
	for _, r := range rep.Results {
		got[r.Profile] = r.Classification
	}
	want := map[string]string{
		"Chrome (Browser)": "allowed",
		"ChatGPT-User":     "blocked",
		"PerplexityBot":    "robots-disallowed",
		"Googlebot":        "allowed",
	}
	for p, c := range want {
		if got[p] != c {
			t.Errorf("%s = %q, want %q", p, got[p], c)
		}
	}
}

func TestCheckAllAllowedExitsZero(t *testing.T) {
	srv := shop(t)
	code, _, stderr := run(t, srv.URL, "-a", "Googlebot", "--no-color")
	if code != output.ExitOK {
		t.Errorf("exit code = %d, want 0 (stderr: %s)", code, stderr)
	}
}

func TestCheckBadInput(t *testing.T) {
	if code, _, _ := run(t, "ftp://example.com"); code != output.ExitError {
		t.Errorf("bad scheme exit code = %d", code)
	}
	if code, _, stderr := run(t, "https://example.com", "-a", "NoSuchBot"); code != output.ExitError || !strings.Contains(stderr, "no profiles match") {
		t.Errorf("unknown agent: code=%d stderr=%q", code, stderr)
	}
}

func TestCheckRobotsOnlyTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: Google-Extended\nDisallow: /\n"))
			return
		}
		if r.UserAgent() == "" || strings.Contains(r.UserAgent(), "Google-Extended") {
			t.Errorf("robots-only token was fetched with UA %q", r.UserAgent())
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	_, stdout, _ := run(t, srv.URL, "-f", "json", "-a", "Google-Extended,Applebot-Extended,Googlebot")
	var rep types.ComparisonResult
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rep.Results {
		got[r.Profile] = r.Classification
	}
	if got["Google-Extended"] != "robots-disallowed" || got["Applebot-Extended"] != "allowed" || got["Googlebot"] != "allowed" {
		t.Errorf("classifications = %v", got)
	}
}

func TestProfilesList(t *testing.T) {
	code, stdout, stderr := run(t, "profiles", "list", "--category", "ai-assistant")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{"ChatGPT-User", "Claude-User", "baseline"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q", want)
		}
	}
	if strings.Contains(stdout, "GPTBot") {
		t.Error("category filter did not exclude GPTBot")
	}
}

func TestProfilesShow(t *testing.T) {
	code, stdout, _ := run(t, "profiles", "show", "gptbot")
	if code != 0 || !strings.Contains(stdout, "robots_key: GPTBot") {
		t.Errorf("code=%d output=%s", code, stdout)
	}
	if code, _, _ := run(t, "profiles", "show", "nobody"); code != output.ExitError {
		t.Errorf("unknown profile exit code = %d", code)
	}
}

func TestRobotsAudit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nContent-Signal: search=yes, ai-train=no\nAllow: /\n\nUser-agent: GPTBot\nDisallow: /\n\nSitemap: https://shop.example/sitemap.xml\n"))
		case "/llms.txt":
			_, _ = w.Write([]byte("# Shop\n\n> Shoes.\n\n## Docs\n\n- [Runners](/runners.md)\n"))
		case "/products/1":
			t.Error("robots audit must not fetch the page itself")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	code, stdout, stderr := run(t, "robots", srv.URL+"/products/1", "-f", "json", "-a", "GPTBot,ClaudeBot,Google-Extended")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	var a types.RobotsAudit
	if err := json.Unmarshal([]byte(stdout), &a); err != nil {
		t.Fatal(err)
	}
	verdict := map[string]bool{}
	for _, ag := range a.Agents {
		verdict[ag.Profile] = ag.Allowed
		if ag.Profile == "ClaudeBot" && ag.Signals["ai-train"] != "no" {
			t.Errorf("ClaudeBot signals = %v", ag.Signals)
		}
	}
	if verdict["GPTBot"] || !verdict["ClaudeBot"] || !verdict["Google-Extended"] {
		t.Errorf("verdicts = %v", verdict)
	}
	if len(a.Sitemaps) != 1 || len(a.LLMsTxt) != 2 || !a.LLMsTxt[0].Found || a.LLMsTxt[0].Links != 1 || a.LLMsTxt[1].Found {
		t.Errorf("sitemaps=%v llms=%+v", a.Sitemaps, a.LLMsTxt)
	}
}

func TestRobotsAuditUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	code, stdout, _ := run(t, "robots", srv.URL, "--no-color", "-a", "GPTBot")
	if code != output.ExitError || !strings.Contains(stdout, "full disallow") {
		t.Errorf("code=%d output=%s", code, stdout)
	}
}

func TestConfigFileAndEnv(t *testing.T) {
	srv := shop(t)
	dir := t.TempDir()
	cfg := dir + "/acurl.yaml"
	if err := os.WriteFile(cfg, []byte("agents: [Googlebot, ChatGPT-User]\nformat: json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACURL_AGENTS", "Googlebot")
	code, stdout, stderr := run(t, "--config", cfg, srv.URL)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	var rep types.ComparisonResult
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("config format: json not applied: %v", err)
	}
	if len(rep.Results) != 1 || rep.Results[0].Profile != "Googlebot" {
		t.Errorf("ACURL_AGENTS should override the config file: %+v", rep.Results)
	}

	if err := os.WriteFile(cfg, []byte("timeuot: 5s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := run(t, "--config", cfg, srv.URL); code != output.ExitError || !strings.Contains(stderr, "timeuot") {
		t.Errorf("typo in config should fail: code=%d stderr=%s", code, stderr)
	}
}

func TestInit(t *testing.T) {
	t.Chdir(t.TempDir())
	if code, _, stderr := run(t, "init"); code != 0 {
		t.Fatalf("init failed: %s", stderr)
	}
	if code, _, stderr := run(t, "init"); code != output.ExitError || !strings.Contains(stderr, "already exists") {
		t.Errorf("second init should refuse: %d %s", code, stderr)
	}
	if _, err := os.Stat(".acurl.yaml"); err != nil {
		t.Error(err)
	}
}

func TestFailOnPolicy(t *testing.T) {
	srv := shop(t)
	args := []string{srv.URL, "-a", "Chrome (Browser),ChatGPT-User,PerplexityBot", "--no-color"}
	if code, _, _ := run(t, append(args, "--fail-on", "none")...); code != 0 {
		t.Errorf("--fail-on none: code %d", code)
	}
	if code, _, stderr := run(t, append(args, "--fail-on", "robots")...); code != output.ExitBlocked || !strings.Contains(stderr, "PerplexityBot robots-disallowed") {
		t.Errorf("--fail-on robots: code %d stderr %s", code, stderr)
	}
	if code, _, _ := run(t, append(args, "--fail-on", "nonsense")...); code != output.ExitError {
		t.Errorf("bad policy: code %d", code)
	}
}

func TestQuietAndVerbose(t *testing.T) {
	srv := shop(t)
	_, _, stderr := run(t, srv.URL, "-a", "Googlebot", "-q", "--no-color")
	if stderr != "" {
		t.Errorf("--quiet should silence stderr, got %q", stderr)
	}
	_, _, stderr = run(t, srv.URL, "-a", "Googlebot", "-v", "--no-color")
	if !strings.Contains(stderr, "> GET "+srv.URL+" [Googlebot] 200") {
		t.Errorf("--verbose should trace requests, got %q", stderr)
	}
}

func TestCompletionAndMan(t *testing.T) {
	code, stdout, stderr := run(t, "completion", "bash")
	if code != 0 || !strings.Contains(stdout, "__start_acurl") {
		t.Errorf("bash completion: code=%d stderr=%s", code, stderr)
	}
	dir := t.TempDir()
	if code, _, stderr := run(t, "man", dir); code != 0 {
		t.Fatalf("man: %s", stderr)
	}
	for _, page := range []string{"acurl.1", "acurl-batch.1", "acurl-robots.1"} {
		if _, err := os.Stat(dir + "/" + page); err != nil {
			t.Errorf("missing man page %s", page)
		}
	}
}

func TestSignedRequests(t *testing.T) {
	dir := t.TempDir()
	keyPath := dir + "/k.pem"
	if code, stdout, stderr := run(t, "keygen", "-o", keyPath); code != 0 || !strings.Contains(stdout, `"kty": "OKP"`) {
		t.Fatalf("keygen: code=%d out=%s err=%s", code, stdout, stderr)
	}
	var gotSig, gotAgent, chromeSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.UserAgent(), "Chrome/") {
			chromeSig = r.Header.Get("Signature")
		} else if r.URL.Path == "/" {
			gotSig, gotAgent = r.Header.Get("Signature-Input"), r.Header.Get("Signature-Agent")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	code, _, stderr := run(t, srv.URL, "-a", "Chrome (Browser),GPTBot", "--no-robots", "--sign", keyPath, "--signature-agent", "https://bot.example", "-q")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(gotSig, `tag="web-bot-auth"`) || gotAgent != `sig1="https://bot.example"` {
		t.Errorf("bot request not signed: input=%q agent=%q", gotSig, gotAgent)
	}
	if chromeSig != "" {
		t.Error("browser baseline must not be signed")
	}
	if code, _, _ := run(t, srv.URL, "--signature-agent", "https://x"); code != output.ExitError {
		t.Error("--signature-agent without --sign should fail")
	}
}

func TestReportFromJSON(t *testing.T) {
	srv := shop(t)
	dir := t.TempDir()
	if code, _, stderr := run(t, srv.URL, "-a", "Chrome (Browser),ChatGPT-User", "-f", "json", "-o", dir+"/run.json", "-q", "--fail-on", "none"); code != 0 {
		t.Fatalf("check: %s", stderr)
	}
	code, stdout, stderr := run(t, "report", dir+"/run.json", "-f", "html")
	if code != 0 || !strings.Contains(stdout, "<!doctype html>") || !strings.Contains(stdout, "tragent.io") || !strings.Contains(stdout, "ChatGPT-User") {
		t.Errorf("report html: code=%d stderr=%s", code, stderr)
	}
	code, stdout, _ = run(t, "report", dir+"/run.json", "-f", "markdown")
	if code != 0 || !strings.Contains(stdout, "| ChatGPT-User | 403 |") {
		t.Errorf("report markdown: %s", stdout)
	}
	if err := os.WriteFile(dir+"/bad.json", []byte(`{"hello":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := run(t, "report", dir+"/bad.json"); code != output.ExitError {
		t.Error("non-report JSON should fail")
	}
}

func TestWatchWithStateFile(t *testing.T) {
	blocked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if blocked && strings.Contains(r.UserAgent(), "ChatGPT-User") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("<html><body><p>Shoes for everyone, shipped free.</p></body></html>"))
	}))
	defer srv.Close()
	state := t.TempDir() + "/state.json"
	args := []string{"watch", srv.URL, "--interval", "0", "--state", state, "-a", "Chrome (Browser),ChatGPT-User", "--no-robots"}

	if code, stdout, stderr := run(t, args...); code != 0 || stdout != "" || !strings.Contains(stderr, "baseline recorded") {
		t.Fatalf("first run: code=%d out=%q err=%q", code, stdout, stderr)
	}
	blocked = true
	code, stdout, _ := run(t, args...)
	if code != 0 || !strings.Contains(stdout, "ChatGPT-User allowed -> blocked") {
		t.Errorf("second run should report the change: %q", stdout)
	}
	if code, _, _ := run(t, "watch", srv.URL, "--interval", "10s"); code != output.ExitError {
		t.Error("intervals under a minute should be rejected")
	}
}

func TestBatchSkipsInvalidLines(t *testing.T) {
	srv := shop(t)
	f := t.TempDir() + "/urls.txt"
	if err := os.WriteFile(f, []byte(srv.URL+"\nnot a url\nftp://x.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, "batch", f, "-f", "json", "-a", "Googlebot", "--no-robots", "--delay", "0", "--fail-on", "blocked,challenged")
	var rep types.BatchReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("invalid JSON (code %d, stderr %s): %v", code, stderr, err)
	}
	if rep.Summary.URLs != 3 || rep.Summary.Failed != 2 || rep.Items[0].Report == nil {
		t.Errorf("summary = %+v", rep.Summary)
	}
	if !strings.Contains(stderr, `skipping "not a url"`) {
		t.Errorf("stderr should name the skipped line: %s", stderr)
	}
	if code != output.ExitError {
		t.Errorf("invalid lines should still make the run exit 2, got %d", code)
	}
}

func TestInvalidFlagValues(t *testing.T) {
	for _, args := range [][]string{
		{"--category", "bogus", "https://example.com"},
		{"--cloak-threshold", "2", "https://example.com"},
		{"-c", "0", "https://example.com"},
		{"--page", "landing", "https://example.com"},
		{"batch", "--parallel", "0", "-"},
	} {
		if code, _, stderr := run(t, args...); code != output.ExitError || stderr == "" {
			t.Errorf("%v: code %d stderr %q, want a usage error", args, code, stderr)
		}
	}
}

func TestReferenceCommand(t *testing.T) {
	code, stdout, _ := run(t, "reference")
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{`id="acurl-batch"`, `id="acurl-version"`, `id="global-flags"`, "--fail-on", "<i>list</i>", "generated by acurl reference"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("reference missing %q", want)
		}
	}
	if strings.Contains(stdout, `id="acurl-reference"`) || strings.Contains(stdout, `id="acurl-man"`) {
		t.Error("hidden commands should not be documented")
	}
}

func TestDiffFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.UserAgent(), "GPTBot") {
			_, _ = w.Write([]byte("<html><body><p>Welcome.</p></body></html>"))
			return
		}
		_, _ = w.Write([]byte("<html><body><p>Wool runner, free shipping.</p></body></html>"))
	}))
	defer srv.Close()
	_, _, stderr := run(t, srv.URL, "-a", "Chrome (Browser),GPTBot", "--no-robots", "--diff", "GPTBot", "--fail-on", "none", "-q")
	if !strings.Contains(stderr, "- Wool runner, free shipping.") || !strings.Contains(stderr, "+ Welcome.") {
		t.Errorf("diff output: %s", stderr)
	}
	if code, _, stderr := run(t, srv.URL, "-a", "GPTBot", "--no-robots", "--diff", "GPTBot", "-q"); code != output.ExitError || !strings.Contains(stderr, "browser baseline") {
		t.Errorf("diff without baseline: code %d, %s", code, stderr)
	}
}

func TestBatchFromSitemapToFile(t *testing.T) {
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nAllow: /\nSitemap: " + base + "/sitemap.xml\n"))
		case "/sitemap.xml":
			_, _ = w.Write([]byte(`<urlset><url><loc>` + base + `/a</loc></url><url><loc>` + base + `/b</loc></url></urlset>`))
		default:
			_, _ = w.Write([]byte("<html><body><p>Page.</p></body></html>"))
		}
	}))
	defer srv.Close()
	base = srv.URL
	out := t.TempDir() + "/batch.json"
	code, _, stderr := run(t, "batch", "--sitemap", "auto:"+srv.URL, "-a", "Googlebot", "--delay", "0", "-f", "json", "-o", out)
	if code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var rep types.BatchReport
	if err := json.Unmarshal(data, &rep); err != nil || rep.Summary.URLs != 2 {
		t.Errorf("sitemap batch: %v %+v", err, rep.Summary)
	}
	if !strings.Contains(stderr, "Found 2 URLs in 1 sitemap") || !strings.Contains(stderr, "Report written to") {
		t.Errorf("stderr: %s", stderr)
	}
	if code, _, _ := run(t, "batch", "--sitemap", "x", "file.txt"); code != output.ExitError {
		t.Error("file and --sitemap together should fail")
	}
}

func TestExitErrorMessage(t *testing.T) {
	if got := exitError(1).Error(); got != "exit status 1" {
		t.Errorf("Error() = %q", got)
	}
}

func TestTapEnv(t *testing.T) {
	code, stdout, stderr := run(t, "tap", "env", "claude", "--shell", "zsh", "--listen", "127.0.0.1:9000")
	if code != 0 {
		t.Fatalf("exit code = %d (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stdout, "ANTHROPIC_BASE_URL='http://127.0.0.1:9000/anthropic' command claude") || strings.Contains(stdout, "codex") {
		t.Errorf("unexpected wrappers:\n%s", stdout)
	}

	if code, stdout, _ := run(t, "tap", "env", "--list"); code != 0 || !strings.Contains(stdout, "GROK_CLI_CHAT_PROXY_BASE_URL") {
		t.Errorf("--list: code %d, output:\n%s", code, stdout)
	}
}

func TestTapRejectsBadInput(t *testing.T) {
	for _, args := range [][]string{
		{"tap", "--listen", "0.0.0.0:8899"},
		{"tap", "--max-body", "0"},
		{"tap", "env", "cursor"},
		{"tap", "env", "--shell", "tcsh"},
	} {
		if code, _, stderr := run(t, args...); code != output.ExitError {
			t.Errorf("%v: exit code = %d, want %d (stderr: %s)", args, code, output.ExitError, stderr)
		}
	}
}
