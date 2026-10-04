package msp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ByteDeskAI/remote-gateway-plugins/kit"
)

type seen struct{ method, path, query, body, auth string }

func fake(t *testing.T, reply func(r *http.Request) (int, string)) (*kit.Table, *[]seen) {
	t.Helper()
	var log []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		log = append(log, seen{r.Method, r.URL.Path, r.URL.RawQuery, string(b), r.Header.Get("Authorization")})
		code, body := reply(r)
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c := &Client{Domain: "x.firewalla.net", Token: "tok", BaseURL: srv.URL + "/v2"}
	return &kit.Table{Prefix: "firewalla", Ops: Ops(func() *Client { return c }), WritesEnabled: func() bool { return true }}, &log
}

func call(t *testing.T, tb *kit.Table, name, input string, confirm bool) (string, error) {
	t.Helper()
	out, err := tb.Call(context.Background(), name, json.RawMessage(input), confirm)
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal(out)
	return string(raw), nil
}

func TestEndpoints(t *testing.T) {
	tb, log := fake(t, func(*http.Request) (int, string) { return 200, `[]` })
	cases := []struct{ op, in, method, path, query, body string }{
		{"boxes.list", `{"group":"7"}`, "GET", "/v2/boxes", "group=7", ""},
		{"devices.list", `{"box":"g1"}`, "GET", "/v2/devices", "box=g1", ""},
		{"devices.rename", `{"gid":"g1","id":"AA:BB","name":"tv"}`, "PATCH", "/v2/boxes/g1/devices/AA:BB", "", `{"name":"tv"}`},
		{"rules.list", `{"query":"status:active"}`, "GET", "/v2/rules", "query=status%3Aactive", ""},
		{"rules.create", `{"rule":{"action":"block","gid":"g1"}}`, "POST", "/v2/rules", "", `{"action":"block","gid":"g1"}`},
		{"rules.pause", `{"id":"r1"}`, "POST", "/v2/rules/r1/pause", "", ""},
		{"rules.resume", `{"id":"r1"}`, "POST", "/v2/rules/r1/resume", "", ""},
		{"rules.delete", `{"id":"r1"}`, "DELETE", "/v2/rules/r1", "", ""},
		{"alarms.get", `{"gid":"g1","aid":5}`, "GET", "/v2/alarms/g1/5", "", ""},
		{"alarms.archive", `{"gid":"g1","aid":"5"}`, "POST", "/v2/alarms/g1/5/archive", "", ""},
		{"alarms.mute", `{"gid":"g1","aid":5,"target":{"type":"domain","value":"a.com"},"scope":{"type":"device","value":"AA"}}`, "POST", "/v2/alarms/g1/5/mute", "", `{"scope":{"type":"device","value":"AA"},"target":{"type":"domain","value":"a.com"}}`},
		{"alarms.delete", `{"gid":"g1","aid":5}`, "DELETE", "/v2/alarms/g1/5", "", ""},
		{"stats.simple", `{}`, "GET", "/v2/stats/simple", "", ""},
		{"stats.top", `{"type":"topBoxesByBlockedFlows","limit":3}`, "GET", "/v2/stats/topBoxesByBlockedFlows", "limit=3", ""},
		{"targetlists.list", `{}`, "GET", "/v2/target-lists", "", ""},
		{"targetlists.get", `{"id":"TL-1"}`, "GET", "/v2/target-lists/TL-1", "", ""},
		{"targetlists.create", `{"list":{"name":"n","targets":["a.com"],"owner":"global"}}`, "POST", "/v2/target-lists", "", `{"name":"n","targets":["a.com"],"owner":"global"}`},
		{"targetlists.update", `{"id":"TL-1","list":{"targets":["b.com"]}}`, "PATCH", "/v2/target-lists/TL-1", "", `{"targets":["b.com"]}`},
		{"targetlists.delete", `{"id":"TL-1"}`, "DELETE", "/v2/target-lists/TL-1", "", ""},
	}
	for _, c := range cases {
		if _, err := call(t, tb, c.op, c.in, true); err != nil {
			t.Fatalf("%s: %v", c.op, err)
		}
		got := (*log)[len(*log)-1]
		want := seen{c.method, c.path, c.query, c.body, "Token tok"}
		if got != want {
			t.Fatalf("%s:\n got %+v\nwant %+v", c.op, got, want)
		}
	}
}

func TestValidation(t *testing.T) {
	tb, log := fake(t, func(*http.Request) (int, string) { return 200, `{}` })
	bad := map[string]string{
		"devices.rename": `{"gid":"g","id":"d","name":"` + strings.Repeat("x", 33) + `"}`,
		"rules.create":   `{"rule":{"action":"redirect"}}`,
		"rules.delete":   `{}`,
		"stats.top":      `{"type":"nope"}`,
		"flows.list":     `{"limit":501}`,
		"alarms.mute":    `{"gid":"g","aid":1}`,
	}
	for op, in := range bad {
		if _, err := call(t, tb, op, in, true); err == nil {
			t.Fatalf("%s accepted %s", op, in)
		}
	}
	if len(*log) != 0 {
		t.Fatalf("invalid input reached the API: %v", *log)
	}
}

func TestFlowPaging(t *testing.T) {
	tb, log := fake(t, func(r *http.Request) (int, string) {
		switch r.URL.Query().Get("cursor") {
		case "":
			return 200, `{"count":1,"results":[{"ts":1}],"next_cursor":"c2"}`
		case "c2":
			return 200, `{"count":1,"results":[{"ts":2}],"next_cursor":"c3"}`
		default:
			return 200, `{"count":1,"results":[{"ts":3}],"next_cursor":null}`
		}
	})
	out, err := call(t, tb, "flows.list", `{"limit":500,"maxPages":2,"query":"box.id:g1"}`, false)
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"count":2,"next_cursor":"c3","results":[{"ts":1},{"ts":2}]}` {
		t.Fatalf("out = %s", out)
	}
	if (*log)[0].query != "limit=500&query=box.id%3Ag1" || (*log)[1].query != "cursor=c2&limit=500&query=box.id%3Ag1" {
		t.Fatalf("queries = %+v", *log)
	}
	out, _ = call(t, tb, "flows.list", `{"cursor":"c3","maxPages":5}`, false)
	if out != `{"count":1,"next_cursor":null,"results":[{"ts":3}]}` {
		t.Fatalf("last page = %s", out)
	}
}

func TestErrors(t *testing.T) {
	tb, _ := fake(t, func(*http.Request) (int, string) { return 401, `{"msg":"bad token"}` })
	if _, err := call(t, tb, "boxes.list", `{}`, false); err == nil || !strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), "tok ") {
		t.Fatalf("err = %v", err)
	}
	empty := &kit.Table{Ops: Ops(func() *Client { return &Client{} })}
	if _, err := empty.Call(context.Background(), "boxes.list", nil, false); err != ErrNotConfigured {
		t.Fatalf("unconfigured = %v", err)
	}
}
