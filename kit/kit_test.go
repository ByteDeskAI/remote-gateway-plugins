package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type renameIn struct {
	ID    string `json:"id" kit:"required" desc:"device id"`
	Limit int    `json:"limit"`
	Force bool   `json:"force"`
}

func table(writes bool) (*Table, *[]string) {
	var calls []string
	h := func(name string) func(context.Context, json.RawMessage) (any, error) {
		return func(_ context.Context, in json.RawMessage) (any, error) {
			calls = append(calls, name+" "+string(in))
			return map[string]string{"ok": name}, nil
		}
	}
	return &Table{
		Prefix:        "fw",
		WritesEnabled: func() bool { return writes },
		Ops: []Op{
			{Name: "devices.list", Summary: "List", Risk: Read, Input: renameIn{}, Handle: h("list")},
			{Name: "devices.rename", Summary: "Rename", Risk: Write, Input: renameIn{}, Handle: h("rename")},
			{Name: "rules.delete", Summary: "Delete", Risk: Destructive, Handle: h("delete")},
		},
	}, &calls
}

func TestWriteGate(t *testing.T) {
	ctx := context.Background()
	off, _ := table(false)
	if _, err := off.Call(ctx, "devices.rename", nil, true); err != ErrWritesDisabled {
		t.Fatalf("writes off + confirm: %v", err)
	}
	on, calls := table(true)
	if _, err := on.Call(ctx, "rules.delete", nil, false); err != ErrNeedsConfirm {
		t.Fatalf("no confirm: %v", err)
	}
	if _, err := on.Call(ctx, "rules.delete", nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := off.Call(ctx, "devices.list", nil, false); err != nil {
		t.Fatalf("read gated: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestMCP(t *testing.T) {
	tb, calls := table(true)
	tools := tb.MCPTools()
	if len(tools) != 3 || tools[1]["name"] != "fw_devices_rename" {
		t.Fatalf("tools = %v", tools)
	}
	schema := tools[1]["inputSchema"].(map[string]any)
	if req := schema["required"].([]string); len(req) != 2 || req[1] != "confirm" {
		t.Fatalf("required = %v", req)
	}
	out := string(tb.MCPCall(context.Background(), []byte(`{"name":"fw_devices_rename","arguments":{"id":"a"}}`)))
	if !strings.Contains(out, "confirm") || !strings.Contains(out, `"error"`) {
		t.Fatalf("unconfirmed write = %s", out)
	}
	out = string(tb.MCPCall(context.Background(), []byte(`{"name":"fw_devices_rename","arguments":{"id":"a","confirm":true}}`)))
	if out != `{"result":{"ok":"rename"}}` {
		t.Fatalf("confirmed write = %s", out)
	}
	if (*calls)[0] != `rename {"id":"a"}` {
		t.Fatalf("confirm leaked into input: %v", *calls)
	}
	if out := string(tb.MCPCall(context.Background(), []byte(`{"name":"nope"}`))); !strings.Contains(out, "unknown tool") {
		t.Fatal(out)
	}
}

func TestHTTP(t *testing.T) {
	tb, calls := table(true)
	srv := httptest.NewServer(tb.Handler())
	defer srv.Close()
	res, _ := http.Get(srv.URL + "/api/devices/list?id=x&limit=5&force=true")
	if res.StatusCode != 200 {
		t.Fatalf("GET read = %d", res.StatusCode)
	}
	if (*calls)[0] != `list {"force":true,"id":"x","limit":5}` {
		t.Fatalf("query input = %v", *calls)
	}
	if res, _ := http.Get(srv.URL + "/api/devices/rename?id=x"); res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET write = %d", res.StatusCode)
	}
	if res, _ := http.Post(srv.URL+"/api/devices/rename", "application/json", strings.NewReader(`{"id":"x"}`)); res.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("POST unconfirmed = %d", res.StatusCode)
	}
	if res, _ := http.Post(srv.URL+"/api/devices/rename", "application/json", strings.NewReader(`{"id":"x","confirm":true}`)); res.StatusCode != 200 {
		t.Fatalf("POST confirmed = %d", res.StatusCode)
	}
	if res, _ := http.Get(srv.URL + "/api/nope/x"); res.StatusCode != 404 {
		t.Fatalf("unknown = %d", res.StatusCode)
	}
	off, _ := table(false)
	srv2 := httptest.NewServer(off.Handler())
	defer srv2.Close()
	if res, _ := http.Post(srv2.URL+"/api/rules/delete", "application/json", strings.NewReader(`{"confirm":true}`)); res.StatusCode != http.StatusForbidden {
		t.Fatalf("writes off = %d", res.StatusCode)
	}
}

func TestCLI(t *testing.T) {
	ctx := context.Background()
	run := func(tb *Table, args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := tb.RunCLI(ctx, "fw", args, &o, &e)
		return code, o.String(), e.String()
	}
	tb, calls := table(true)
	if code, out, _ := run(tb, "devices", "list", "--id=a", "--limit=3", "--force", "--json"); code != 0 || out != "{\"ok\":\"list\"}\n" {
		t.Fatalf("list = %d %q", code, out)
	}
	if (*calls)[0] != `list {"force":true,"id":"a","limit":3}` {
		t.Fatalf("flags = %v", *calls)
	}
	if code, _, _ := run(tb, "devices", "list", `--input={"id":"b"}`); code != 0 || (*calls)[1] != `list {"id":"b"}` {
		t.Fatalf("--input = %d %v", code, *calls)
	}
	if code, _, errOut := run(tb, "devices", "rename", "--id=a"); code != 1 || !strings.Contains(errOut, "--confirm") {
		t.Fatalf("unconfirmed = %d %q", code, errOut)
	}
	if code, _, _ := run(tb, "devices", "rename", "--id=a", "--confirm"); code != 0 {
		t.Fatalf("confirmed = %d", code)
	}
	for _, bad := range [][]string{{}, {"devices"}, {"nope", "x"}, {"devices", "list", "--limit=x"}, {"devices", "list", "--bogus=1"}, {"devices", "list", "a"}} {
		if code, _, _ := run(tb, bad...); code != 2 {
			t.Fatalf("%v = %d, want usage", bad, code)
		}
	}
}
