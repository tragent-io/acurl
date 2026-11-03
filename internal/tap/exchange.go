package tap

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// Exchange is one request a CLI made through the proxy and the response it got.
type Exchange struct {
	ID         int64         `json:"id"`
	Start      time.Time     `json:"start"`
	Duration   time.Duration `json:"duration_ns"`
	Route      string        `json:"route"`
	Process    string        `json:"process,omitempty"`
	PID        int           `json:"pid,omitempty"`
	Method     string        `json:"method"`
	URL        string        `json:"url"`
	Model      string        `json:"model,omitempty"`
	ReqHeader  http.Header   `json:"request_headers"`
	ReqBody    Body          `json:"request_body"`
	Status     int           `json:"status,omitempty"`
	RespHeader http.Header   `json:"response_headers,omitempty"`
	RespBody   Body          `json:"response_body"`
	Streaming  bool          `json:"streaming,omitempty"`
	Upgrade    string        `json:"upgrade,omitempty"` // e.g. "websocket"; frames are not recorded
	Err        string        `json:"error,omitempty"`
	Canceled   bool          `json:"canceled,omitempty"` // the CLI hung up before the response ended
	Redactions []Redaction   `json:"redactions,omitempty"`
}

// Body is a captured message body. Text holds at most the proxy's body
// limit, decoded when the body had a Content-Encoding; Size is the length
// that passed through on the wire.
type Body struct {
	Text      string `json:"text,omitempty"`
	Size      int64  `json:"size"`
	Encoding  string `json:"encoding,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// capture keeps the first max bytes written to it and counts the rest.
type capture struct {
	mu   sync.Mutex
	max  int
	buf  bytes.Buffer
	size int64
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.size += int64(len(p))
	if room := c.max - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (c *capture) body() Body {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Body{Text: c.buf.String(), Size: c.size, Truncated: c.size > int64(c.buf.Len())}
}

// modelOf finds the model name in a request: the "model" field of a JSON
// body, or the models/<name> path segment Gemini uses.
func modelOf(path string, body Body) string {
	if _, rest, ok := strings.Cut(path, "/models/"); ok {
		name, _, _ := strings.Cut(rest, ":")
		name, _, _ = strings.Cut(name, "/")
		return name
	}
	if !strings.HasPrefix(strings.TrimSpace(body.Text), "{") {
		return ""
	}
	var v struct {
		Model string `json:"model"`
	}
	if json.Unmarshal([]byte(body.Text), &v) == nil {
		return v.Model
	}
	// A body cut at the capture limit is not valid JSON, but providers put
	// "model" near the top, so it is usually in the part that was kept.
	if m := modelField.FindStringSubmatch(body.Text); m != nil {
		var name string
		if json.Unmarshal([]byte(m[1]), &name) == nil {
			return name
		}
	}
	return ""
}

var modelField = regexp.MustCompile(`"model"\s*:\s*("(?:[^"\\]|\\.)*")`)

// decode undoes a Content-Encoding so the body is readable, keeping at most
// limit bytes. A body that cannot be decoded is left as captured.
func decode(b Body, encoding string, limit int) Body {
	enc := strings.ToLower(strings.TrimSpace(encoding))
	if enc == "" || enc == "identity" || b.Text == "" {
		return b
	}
	src := strings.NewReader(b.Text)
	var r io.Reader
	switch enc {
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(src)
		if err != nil {
			return b
		}
		r = zr
	case "deflate":
		zr, err := zlib.NewReader(src)
		if err != nil {
			return b
		}
		r = zr
	case "br":
		r = brotli.NewReader(src)
	case "zstd":
		zr, err := zstd.NewReader(src)
		if err != nil {
			return b
		}
		defer zr.Close()
		r = zr
	default:
		return b
	}
	out, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if len(out) == 0 && err != nil {
		return b
	}
	b.Encoding = enc
	b.Truncated = b.Truncated || len(out) > limit || (err != nil && !errors.Is(err, io.EOF))
	b.Text = string(out[:min(len(out), limit)])
	return b
}
