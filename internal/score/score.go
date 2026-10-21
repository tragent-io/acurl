// Package score turns an acurl report into an AI-readiness score from 0 to
// 100, with the checks behind it.
package score

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"

	"github.com/tragent-io/acurl/internal/classifier"
	"github.com/tragent-io/acurl/pkg/types"
)

// check computes one weighted check. It returns the passed fraction in
// [0, 1], a detail line, and whether the check applies to this page.
type check struct {
	id     string
	title  string
	weight int
	eval   func(rep *types.ComparisonResult) (passed float64, detail string, applies bool)
}

var checks = []check{
	{"assistants-allowed", "AI assistants can fetch the page", 25, allowedShare(types.CategoryAIAssistant)},
	{"ai-search-allowed", "AI search crawlers can fetch the page", 20, allowedShare(types.CategoryAISearch)},
	{"no-challenges", "No bot challenges for AI agents", 10, noChallenges},
	{"no-cloaking", "AI agents see the same content as browsers", 10, noCloaking},
	{"server-rendered", "Content is in the HTML without JavaScript", 15, serverRendered},
	{"structured-data", "Product structured data is present", 15, structuredData},
	{"product-parity", "AI agents see the same price and availability", 10, productParity},
	{"indexable", "Search and AI search may index the page", 15, indexable},
}

// Compute scores rep. Checks that do not apply (for example product checks
// on a page without products) are left out and the rest re-weighted.
func Compute(rep *types.ComparisonResult) *types.Score {
	// Checks is never null in JSON, even when the page is not scored.
	s := &types.Score{Checks: []types.ScoreCheck{}}
	if note := unscorable(rep); note != "" {
		s.Grade = "-"
		s.Note = note
		return s
	}
	total, earned := 0.0, 0.0
	for _, c := range checks {
		passed, detail, applies := c.eval(rep)
		res := types.ScoreCheck{ID: c.id, Title: c.title, Weight: c.weight, Applies: applies, Detail: detail}
		if applies {
			res.Passed = passed >= 1
			if !res.Passed {
				res.Recommendation = recommendations[c.id]
			}
			res.Points = math.Round(passed*float64(c.weight)*10) / 10
			total += float64(c.weight)
			earned += passed * float64(c.weight)
		}
		s.Checks = append(s.Checks, res)
	}
	if total > 0 {
		s.Value = int(math.Round(earned / total * 100))
	}
	s.Grade = grade(s.Value)
	return s
}

func grade(v int) string {
	switch {
	case v >= 90:
		return "A"
	case v >= 80:
		return "B"
	case v >= 65:
		return "C"
	case v >= 50:
		return "D"
	default:
		return "F"
	}
}

// aiAgent reports whether a result belongs to an AI agent category.
func aiAgent(cat string) bool {
	return cat == types.CategoryAIAssistant || cat == types.CategoryAISearch || cat == types.CategoryAITraining
}

func allowedShare(category string) func(*types.ComparisonResult) (float64, string, bool) {
	return func(rep *types.ComparisonResult) (float64, string, bool) {
		var total, ok int
		var bad []string
		for _, r := range rep.Results {
			if r.Category != category || r.RobotsOnly {
				continue
			}
			total++
			if r.Classification == classifier.Allowed {
				ok++
			} else {
				bad = append(bad, r.Profile+" ("+r.Classification+")")
			}
		}
		if total == 0 {
			return 0, "no " + category + " profiles in this run", false
		}
		detail := fmt.Sprintf("%d of %d allowed", ok, total)
		if len(bad) > 0 {
			detail += ": " + strings.Join(bad, ", ")
		}
		return float64(ok) / float64(total), detail, true
	}
}

func countAI(rep *types.ComparisonResult, match func(types.FetchResult) bool) (total int, names []string) {
	for _, r := range rep.Results {
		if !aiAgent(r.Category) || r.RobotsOnly {
			continue
		}
		total++
		if match(r) {
			names = append(names, r.Profile)
		}
	}
	return total, names
}

func noChallenges(rep *types.ComparisonResult) (float64, string, bool) {
	total, hit := countAI(rep, func(r types.FetchResult) bool { return r.Classification == classifier.Challenged })
	if total == 0 {
		return 0, "", false
	}
	if len(hit) == 0 {
		return 1, "no challenge pages", true
	}
	return 1 - float64(len(hit))/float64(total), "challenged: " + strings.Join(hit, ", "), true
}

func noCloaking(rep *types.ComparisonResult) (float64, string, bool) {
	if classifier.BaselineIndex(rep.Results) < 0 {
		return 0, "needs the browser baseline", false
	}
	total, hit := countAI(rep, func(r types.FetchResult) bool { return r.Classification == classifier.Cloaked })
	if total == 0 {
		return 0, "", false
	}
	if len(hit) == 0 {
		return 1, "content matches the browser", true
	}
	return 1 - float64(len(hit))/float64(total), "different content for: " + strings.Join(hit, ", "), true
}

func baseline(rep *types.ComparisonResult) *types.FetchResult {
	if b := classifier.BaselineIndex(rep.Results); b >= 0 && rep.Results[b].Classification == classifier.Allowed {
		return &rep.Results[b]
	}
	return nil
}

func serverRendered(rep *types.ComparisonResult) (float64, string, bool) {
	if rr := rep.Render; rr != nil && rr.Error == "" {
		share := rr.JSOnlyShare
		if share < 0.2 {
			return 1, fmt.Sprintf("%.0f%% of text needs JavaScript", share*100), true
		}
		return 1 - share, fmt.Sprintf("%.0f%% of text only appears after JavaScript runs", share*100), true
	}
	b := baseline(rep)
	if b == nil || b.JS == nil {
		return 0, "needs a successful browser baseline", false
	}
	if b.JS.ClientRendered {
		return 0, "client-rendered page: " + b.JS.Reason, true
	}
	return 1, "page text is in the raw HTML", true
}

func structuredData(rep *types.ComparisonResult) (float64, string, bool) {
	b := baseline(rep)
	if b == nil {
		return 0, "needs a successful browser baseline", false
	}
	p := b.Product
	switch {
	case p == nil && isProductPage(rep):
		return 0, "no schema.org Product on what looks like a product page (pass --page other if it is not one)", true
	case p == nil:
		return 0, "no schema.org Product found (fine for non-product pages)", false
	case p.Source != "json-ld":
		return 0.6, "product data only in " + p.Source + "; JSON-LD is the most widely read format", true
	case p.Price == "" || p.Availability == "":
		return 0.7, "JSON-LD Product is missing price or availability", true
	}
	return 1, "JSON-LD Product with price and availability", true
}

func productParity(rep *types.ComparisonResult) (float64, string, bool) {
	b := baseline(rep)
	if b == nil || b.Product == nil {
		return 0, "", false
	}
	total, hit := countAI(rep, func(r types.FetchResult) bool { return r.ProductIssue != "" })
	if total == 0 {
		return 0, "", false
	}
	if len(hit) == 0 {
		return 1, "every allowed AI agent sees the same product data", true
	}
	return 1 - float64(len(hit))/float64(total), "different product data for: " + strings.Join(hit, ", "), true
}

// unscorable explains why a report cannot be scored, or returns "". Without
// a working browser baseline there is nothing to compare agents with, and a
// score for an image or a JSON document would mean nothing.
func unscorable(rep *types.ComparisonResult) string {
	b := classifier.BaselineIndex(rep.Results)
	if b < 0 {
		return ""
	}
	r := rep.Results[b]
	switch r.Classification {
	case classifier.Allowed:
		if ct := r.ContentType; ct != "" && !strings.Contains(strings.ToLower(ct), "html") {
			return "the page is " + mediaType(ct) + ", not HTML, so there is nothing for an agent to read; check an HTML page instead"
		}
		return ""
	case classifier.Error:
		return "the browser request failed (" + shortError(r.Error) + "), so the page could not be scored"
	case classifier.Blocked:
		return fmt.Sprintf("the browser got HTTP %d, so the page could not be scored; check the URL, or whether the site blocks this network", r.StatusCode)
	case classifier.Challenged:
		return "the browser baseline was challenged by " + r.BotChallenge +
			", so the site is treating this network or IP as a bot; scores would not reflect AI agent access"
	case classifier.RateLimited:
		return "the browser baseline was rate limited (HTTP 429); wait and run again"
	}
	return "the browser baseline was " + r.Classification + ", so the page could not be scored"
}

func mediaType(ct string) string {
	mt, _, _ := strings.Cut(ct, ";")
	return strings.TrimSpace(mt)
}

// shortError turns a Go network error into a phrase a person recognises.
func shortError(err string) string {
	e := strings.ToLower(err)
	switch {
	case strings.Contains(e, "no such host"), strings.Contains(e, "server misbehaving"):
		return "DNS lookup failed: the host does not resolve"
	case strings.Contains(e, "private or internal address"):
		return "the host resolved to a private or internal address"
	case strings.Contains(e, "timeout"), strings.Contains(e, "deadline exceeded"):
		return "timed out"
	case strings.Contains(e, "connection refused"):
		return "connection refused"
	case strings.Contains(e, "certificate"), strings.Contains(e, "tls"):
		return "TLS error: " + truncate(err, 80)
	case strings.Contains(e, "redirects"):
		return "too many redirects"
	}
	return truncate(err, 80)
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// productPath matches URL paths storefront platforms use for product pages.
var productPath = regexp.MustCompile(`(?i)(/products?/[^/?#]+|/p/[^/?#]+|/dp/[a-z0-9]{6,}|/gp/product/|/items?/[^/?#]+|-p-\d+|/sku/)`)

// isProductPage reports whether the score should expect product data:
// the caller said so, or the URL or page looks like a product page.
func isProductPage(rep *types.ComparisonResult) bool {
	switch rep.Page {
	case types.PageProduct:
		return true
	case types.PageOther:
		return false
	}
	if b := baseline(rep); b != nil && b.Product != nil {
		return true
	}
	if u, err := url.Parse(rep.URL); err == nil {
		return productPath.MatchString(u.Path)
	}
	return false
}

func indexable(rep *types.ComparisonResult) (float64, string, bool) {
	b := baseline(rep)
	if b == nil {
		return 0, "", false
	}
	// The browser row carries the generic robots directives; agent rows add
	// directives addressed to that agent by name.
	if d := blocking(b.Directives); d != "" {
		return 0, d + " in meta robots or X-Robots-Tag: search and AI search will drop the page", true
	}
	var total int
	var hit []string
	for _, r := range rep.Results {
		if !aiAgent(r.Category) || r.RobotsOnly ||
			(r.Classification != classifier.Allowed && r.Classification != classifier.Cloaked) {
			continue
		}
		total++
		if blocking(r.Directives) != "" {
			hit = append(hit, r.Profile)
		}
	}
	if total == 0 {
		return 1, "no noindex directives", true
	}
	if len(hit) > 0 {
		return 1 - float64(len(hit))/float64(total), "noindex for: " + strings.Join(hit, ", "), true
	}
	return 1, "no noindex directives", true
}

func blocking(ds []string) string {
	for _, d := range ds {
		if d == "noindex" || d == "none" {
			return d
		}
	}
	return ""
}
