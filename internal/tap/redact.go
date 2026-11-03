package tap

import (
	"net/http"
	"net/url"
	"strings"
)

// Redacted replaces every removed value.
const Redacted = "[redacted]"

// Redaction records where a value was removed from a capture and why, so a
// reader can tell a redacted field from an empty one.
type Redaction struct {
	Where  string `json:"where"`  // e.g. "request.header.x-api-key"
	Reason string `json:"reason"` // "credential" or "user secret"
}

// credentialHeaders always carry secrets.
var credentialHeaders = []string{
	"Authorization", "Proxy-Authorization", "X-Api-Key", "Anthropic-Api-Key",
	"X-Goog-Api-Key", "Cookie", "Set-Cookie",
}

// credentialParams carry secrets in query strings (Gemini accepts ?key=).
var credentialParams = []string{"key", "api_key", "access_token"}

// Redact replaces each of secrets in x's headers, URL, bodies and process name, and
// records where it did.
func Redact(x *Exchange, secrets []string) {
	redactor{secrets: secrets}.apply(x)
}

// redactor removes credentials and user-supplied secrets from an exchange.
type redactor struct {
	credentials bool // remove credentialHeaders and credentialParams
	secrets     []string
}

func (r redactor) apply(x *Exchange) {
	x.Process = r.text(x, "process", x.Process)
	r.header(x, "request", x.ReqHeader)
	r.header(x, "response", x.RespHeader)
	x.URL = r.query(x, x.URL)
	x.ReqBody.Text = r.text(x, "request.body", x.ReqBody.Text)
	x.RespBody.Text = r.text(x, "response.body", x.RespBody.Text)
}

func (r redactor) header(x *Exchange, side string, h http.Header) {
	for _, name := range credentialHeaders {
		if vs, ok := h[name]; ok && r.credentials {
			for i := range vs {
				vs[i] = Redacted
			}
			x.Redactions = append(x.Redactions, Redaction{Where: side + ".header." + strings.ToLower(name), Reason: "credential"})
		}
	}
	for name, vs := range h {
		for i, v := range vs {
			vs[i] = r.text(x, side+".header."+strings.ToLower(name), v)
		}
	}
}

func (r redactor) query(x *Exchange, raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return r.text(x, "request.url", raw)
	}
	q := u.Query()
	changed := false
	for _, p := range credentialParams {
		if q.Has(p) && r.credentials {
			q.Set(p, Redacted)
			changed = true
			x.Redactions = append(x.Redactions, Redaction{Where: "request.query." + p, Reason: "credential"})
		}
	}
	if changed {
		u.RawQuery = q.Encode()
		raw = u.String()
	}
	return r.text(x, "request.url", raw)
}

func (r redactor) text(x *Exchange, where, s string) string {
	for _, secret := range r.secrets {
		if secret != "" && strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, Redacted)
			x.Redactions = append(x.Redactions, Redaction{Where: where, Reason: "user secret"})
		}
	}
	return s
}
