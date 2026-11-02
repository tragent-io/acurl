package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tragent-io/acurl/pkg/acurl"
)

func TestServeProtocolErrors(t *testing.T) {
	srv, err := New(acurl.Options{BlockPrivate: true}, "test")
	if err != nil {
		t.Fatal(err)
	}

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"ping","id":1}`,
		`{"jsonrpc":`,
		`{"jsonrpc":"1.0","method":"ping","id":2}`,
		`{"jsonrpc":"2.0","id":3}`,
		`{"jsonrpc":"2.0","method":"missing/method","id":4}`,
		`{"jsonrpc":"2.0","method":"missing/notification"}`,
	}, "\n") + "\n"

	var output bytes.Buffer
	if err := srv.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d responses, want 5: %s", len(lines), output.String())
	}

	wantIDs := []any{float64(1), nil, float64(2), float64(3), float64(4)}
	wantCodes := []float64{0, -32700, -32600, -32600, -32601}
	for i, line := range lines {
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("response %d is invalid JSON: %v", i, err)
		}
		if got["id"] != wantIDs[i] {
			t.Errorf("response %d id = %v, want %v", i, got["id"], wantIDs[i])
		}
		if i == 0 {
			if got["result"] == nil {
				t.Fatalf("ping response has no result: %v", got)
			}
			continue
		}
		e, ok := got["error"].(map[string]any)
		if !ok {
			t.Fatalf("response %d has no error object: %v", i, got)
		}
		if e["code"] != wantCodes[i] {
			t.Errorf("response %d error code = %v, want %v", i, e["code"], wantCodes[i])
		}
	}
}

func TestToolsCallInvalidParamsAndArguments(t *testing.T) {
	srv, err := New(acurl.Options{BlockPrivate: true}, "test")
	if err != nil {
		t.Fatal(err)
	}

	res := srv.dispatch(context.Background(), request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "tools/call",
		Params:  json.RawMessage("{"),
	})
	if res.Error == nil || res.Error.Code != codeInvalidParams {
		t.Fatalf("malformed tools/call params error = %v, want code %d", res.Error, codeInvalidParams)
	}

	res = srv.dispatch(context.Background(), request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("2"),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"unknown_tool"}`),
	})
	if res.Error == nil || res.Error.Code != codeInvalidParams {
		t.Fatalf("unknown tool error = %v, want code %d", res.Error, codeInvalidParams)
	}

	res = srv.dispatch(context.Background(), request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("3"),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"check_url","arguments":[]}`),
	})
	if res.Error != nil || res.Result == nil {
		t.Fatalf("invalid tool arguments should be returned as a tool result: %+v", res)
	}
	result, ok := res.Result.(*ToolResult)
	if !ok || !result.IsError || len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "invalid arguments") {
		t.Fatalf("invalid arguments result = %#v, want an isError tool result", res.Result)
	}
}

func TestToolArgumentErrorPaths(t *testing.T) {
	ctx := context.Background()
	invalid := json.RawMessage("{")

	toolSet := &tools{base: acurl.Options{BlockPrivate: true}}
	for name, handler := range map[string]func(context.Context, json.RawMessage) (*ToolResult, error){
		"check":  toolSet.check,
		"score":  toolSet.score,
		"robots": toolSet.robots,
		"list":   toolSet.list,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := handler(ctx, invalid)
			if err == nil || !strings.Contains(err.Error(), "invalid arguments") {
				t.Fatalf("handler error = %v, want invalid arguments", err)
			}
		})
	}

	if _, err := toolSet.check(ctx, json.RawMessage(`{}`)); err == nil || err.Error() != "url is required" {
		t.Errorf("missing URL error = %v, want url is required", err)
	}
	if _, err := toolSet.run(ctx, urlArgs{URL: "https://example.com", Categories: []string{"not-a-category"}}); err == nil || !strings.Contains(err.Error(), "no profiles match") {
		t.Errorf("empty profile selection error = %v, want no profiles match", err)
	}
}

func TestParseArgsNormalizationAndErrors(t *testing.T) {
	args, err := parseArgs(json.RawMessage(`{"url":"  example.com/path  ","category":"search"}`), true)
	if err != nil {
		t.Fatal(err)
	}
	if args.URL != "https://example.com/path" || args.Category != "search" {
		t.Errorf("normalized args = %+v", args)
	}

	if _, err := parseArgs(json.RawMessage(`{"url":"   "}`), true); err == nil || err.Error() != "url is required" {
		t.Errorf("blank URL error = %v, want url is required", err)
	}
	if _, err := parseArgs(json.RawMessage(`{"url":42}`), true); err == nil {
		t.Error("non-string URL should be rejected")
	}
	if _, err := parseArgs(nil, false); err != nil {
		t.Errorf("empty optional arguments should be accepted: %v", err)
	}
}
