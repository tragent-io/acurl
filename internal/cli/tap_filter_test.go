package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tragent-io/acurl/internal/tap"
)

func TestTapProcessFilter(t *testing.T) {
	f, err := parseTapProcessFilter([]string{"NoDe:42", "claude", "My CLI:7"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		pid  int
		want bool
	}{
		{"node", 42, true}, {"NODE", 43, false}, {"claude", 99, true}, {"my cli", 7, true},
		{"claude-helper", 99, false}, {"node", 0, false}, {"", 42, false}, {"", 0, false},
	} {
		x := &tap.Exchange{Process: tt.name, PID: tt.pid}
		if got := f.matches(x); got != tt.want {
			t.Errorf("matches(%q:%d) = %v", tt.name, tt.pid, got)
		}
		if !(tapProcessFilter(nil)).matches(x) {
			t.Errorf("no filter excludes %q:%d", tt.name, tt.pid)
		}
	}
}

func TestTapProcessFilterInvalid(t *testing.T) {
	for _, value := range []string{"", ":42", " node", "node ", "node:", "node:0", "node:-1", "node:+1", "node:1.5", "node:1:2", "node: 1", "node:9999999999999999999999999999", "node\n", "node\x1b", "node\u202e"} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseTapProcessFilter([]string{value}); err == nil {
				t.Errorf("accepted %q", value)
			}
		})
	}
}

func TestTapProcessFlagRepeatable(t *testing.T) {
	cmd := newTapCmd(&app{})
	if err := cmd.ParseFlags([]string{"--process", "node:42", "--process", "cli,with,commas"}); err != nil {
		t.Fatal(err)
	}
	values, err := cmd.Flags().GetStringArray("process")
	if err != nil || len(values) != 2 || values[0] != "node:42" || values[1] != "cli,with,commas" {
		t.Fatalf("--process values = %q, error %v", values, err)
	}
}

func TestWatchTapFiltersLocallyAfterRedaction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != tap.EventsPath || r.URL.RawQuery != "" {
			t.Errorf("viewer sent filtering to server: %s", r.URL)
		}
		enc := json.NewEncoder(w)
		for _, x := range []*tap.Exchange{{ID: 1, Process: "acme-cli", PID: 42}, {ID: 2, Process: "other", PID: 43}} {
			if err := enc.Encode(tap.Event{Exchange: x}); err != nil {
				t.Error(err)
			}
		}
	}))
	defer srv.Close()
	var out, errb bytes.Buffer
	a := &app{stdout: &out, stderr: &errb}
	filter, err := parseTapProcessFilter([]string{tap.Redacted + "-cli:42"})
	if err != nil {
		t.Fatal(err)
	}
	sink, closeSink, err := a.tapSink("-", false, filter)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSink()
	if err := a.watchTap(context.Background(), strings.TrimPrefix(srv.URL, "http://"), []string{"acme"}, sink); err != nil {
		t.Fatal(err)
	}
	var x tap.Exchange
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &x); err != nil || x.ID != 1 || x.Process != tap.Redacted+"-cli" {
		t.Fatalf("redacted filtered stream = %s, error %v", &out, err)
	}
}

func TestTapSinkProcessFilter(t *testing.T) {
	for _, mode := range []string{"lines", "stdout-json", "file"} {
		t.Run(mode, func(t *testing.T) {
			var out, errb bytes.Buffer
			a := &app{stdout: &out, stderr: &errb}
			f, err := parseTapProcessFilter([]string{"node:42"})
			if err != nil {
				t.Fatal(err)
			}
			write := ""
			if mode == "stdout-json" {
				write = "-"
			}
			if mode == "file" {
				write = filepath.Join(t.TempDir(), "all.jsonl")
			}
			sink, closeSink, err := a.tapSink(write, true, f)
			if err != nil {
				t.Fatal(err)
			}
			for i, process := range []string{"node", "claude", ""} {
				sink(&tap.Exchange{ID: int64(i + 1), Process: process, PID: 42, Route: "anthropic", Method: "POST", URL: "https://example.com/", ReqBody: tap.Body{Text: []string{"matched body", "excluded body", "unknown body"}[i], Size: 12}})
			}
			closeSink()
			if errb.Len() != 0 {
				t.Fatalf("stderr: %s", &errb)
			}
			if strings.Contains(out.String(), "excluded body") || strings.Contains(out.String(), "unknown body") {
				t.Errorf("excluded body displayed: %s", &out)
			}
			if !strings.Contains(out.String(), "matched body") {
				t.Errorf("matched body missing: %s", &out)
			}
			if mode == "stdout-json" {
				var x tap.Exchange
				if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &x); err != nil || x.ID != 1 {
					t.Fatalf("filtered JSON = %s, error %v", &out, err)
				}
			} else if strings.Count(out.String(), "anthropic POST /") != 1 {
				t.Errorf("filtered lines: %s", &out)
			}
			if mode == "file" {
				b, err := os.ReadFile(write)
				if err != nil {
					t.Fatal(err)
				}
				lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
				if len(lines) != 3 {
					t.Fatalf("capture lost exchanges: %s", b)
				}
				for i, line := range lines {
					var x tap.Exchange
					if err := json.Unmarshal(line, &x); err != nil || x.ID != int64(i+1) {
						t.Errorf("capture %s: %v", line, err)
					}
				}
			}
		})
	}
}
