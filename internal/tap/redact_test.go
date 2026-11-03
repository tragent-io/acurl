package tap

import (
	"net/http"
	"strings"
	"testing"
)

func TestRedactor(t *testing.T) {
	x := &Exchange{
		URL: "https://generativelanguage.googleapis.com/v1beta/models/m:generateContent?alt=sse&key=AIza-secret",
		ReqHeader: http.Header{
			"Authorization":  {"Bearer sk-ant-oat-secret"},
			"X-Goog-Api-Key": {"AIza-secret"},
			"User-Agent":     {"claude-cli/2.0 (team-acme)"},
		},
		RespHeader: http.Header{"Set-Cookie": {"session=abc"}},
		ReqBody:    Body{Text: `{"metadata":{"user_id":"team-acme"}}`},
	}
	redactor{credentials: true, secrets: []string{"team-acme"}}.apply(x)

	for _, v := range []string{x.ReqHeader.Get("Authorization"), x.ReqHeader.Get("X-Goog-Api-Key"), x.RespHeader.Get("Set-Cookie")} {
		if v != Redacted {
			t.Errorf("credential header = %q, want %q", v, Redacted)
		}
	}
	if strings.Contains(x.URL, "AIza") || !strings.Contains(x.URL, "alt=sse") {
		t.Errorf("URL = %q, want key removed and alt kept", x.URL)
	}
	if strings.Contains(x.ReqHeader.Get("User-Agent"), "team-acme") || strings.Contains(x.ReqBody.Text, "team-acme") {
		t.Errorf("user secret left in %q / %q", x.ReqHeader.Get("User-Agent"), x.ReqBody.Text)
	}
	where := map[string]string{}
	for _, r := range x.Redactions {
		where[r.Where] = r.Reason
	}
	for w, reason := range map[string]string{
		"request.header.authorization":  "credential",
		"request.header.x-goog-api-key": "credential",
		"response.header.set-cookie":    "credential",
		"request.query.key":             "credential",
		"request.header.user-agent":     "user secret",
		"request.body":                  "user secret",
	} {
		if where[w] != reason {
			t.Errorf("redaction %s = %q, want %q (all: %v)", w, where[w], reason, x.Redactions)
		}
	}
}

func TestRedactorKeepCredentials(t *testing.T) {
	x := &Exchange{
		URL:       "https://example.com/v1?key=k&b=2&a=1",
		ReqHeader: http.Header{"Authorization": {"Bearer t"}},
	}
	redactor{}.apply(x)
	if x.ReqHeader.Get("Authorization") != "Bearer t" || x.URL != "https://example.com/v1?key=k&b=2&a=1" || len(x.Redactions) != 0 {
		t.Errorf("exchange changed with credentials kept: %+v", x)
	}
}

func TestRedactProcess(t *testing.T) {
	x := &Exchange{Process: "team-acme-cli", PID: 42}
	Redact(x, []string{"team-acme"})
	if x.Process != Redacted+"-cli" || x.PID != 42 {
		t.Errorf("process metadata = %q:%d", x.Process, x.PID)
	}
	if len(x.Redactions) != 1 || x.Redactions[0] != (Redaction{Where: "process", Reason: "user secret"}) {
		t.Errorf("redactions = %+v", x.Redactions)
	}
}
