package tap

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWriteLineProcess(t *testing.T) {
	for _, tt := range []struct {
		name  string
		pid   int
		label string
	}{
		{"", 0, ""},
		{"", 42, ""},
		{"node", 0, ""},
		{"node", 42, " node:42"},
		{"evil\n\r\t\x1b[2J\u202e", 42, ` evil\n\r\t\x1b[2J\u202e:42`},
	} {
		t.Run(tt.name+tt.label, func(t *testing.T) {
			x := &Exchange{Start: time.Date(2026, 1, 1, 12, 1, 3, 0, time.UTC), ID: 4, Route: "anthropic", Method: "POST", URL: "https://example.com/v1/messages", Process: tt.name, PID: tt.pid}
			var b bytes.Buffer
			if err := WriteLine(&b, x); err != nil {
				t.Fatal(err)
			}
			want := "12:01:03 #4" + tt.label + " anthropic POST /v1/messages 0s 0B > 0B\n"
			if b.String() != want {
				t.Errorf("got %q, want %q", b.String(), want)
			}
		})
	}
}

func TestExchangeOlderJSONHasUnknownProcess(t *testing.T) {
	var x Exchange
	if err := json.Unmarshal([]byte(`{"id":4,"route":"anthropic","method":"GET","url":"https://example.com/"}`), &x); err != nil {
		t.Fatal(err)
	}
	if x.Process != "" || x.PID != 0 {
		t.Fatalf("older exchange has process metadata: %+v", x)
	}
	b, err := json.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"process"`) || strings.Contains(string(b), `"pid"`) {
		t.Errorf("unknown metadata should be omitted: %s", b)
	}
}
