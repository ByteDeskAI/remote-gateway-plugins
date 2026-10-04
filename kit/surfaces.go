package kit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// ---- MCP ----------------------------------------------------------------
//
// Shape the gateway's spawned-plugin MCP adapter expects
// (bytedesk-remote-gateway src/mcp_service_provider.go): the "tools" endpoint
// replies {"tools":[{name,description,inputSchema}]}; the "call" endpoint takes
// {"name","arguments"} and replies {"result":…} or {"error":"…"}.

// ToolName is the MCP tool name for an op.
func (t *Table) ToolName(o Op) string {
	return t.Prefix + "_" + strings.ReplaceAll(o.Name, ".", "_")
}

// MCPTools lists every op as an MCP tool descriptor.
func (t *Table) MCPTools() []map[string]any {
	out := make([]map[string]any, 0, len(t.Ops))
	for _, o := range t.Ops {
		desc := o.Summary
		if o.Risk != Read {
			desc += " (" + string(o.Risk) + ": requires write tools enabled and confirm: true)"
		}
		out = append(out, map[string]any{"name": t.ToolName(o), "description": desc, "inputSchema": o.Schema()})
	}
	return out
}

// MCPToolsReply is the "tools" endpoint body.
func (t *Table) MCPToolsReply() []byte {
	raw, _ := json.Marshal(map[string]any{"tools": t.MCPTools()})
	return raw
}

// MCPCall answers one "call" endpoint request body. It never fails: errors
// travel in the reply's "error" field, as the host adapter expects.
func (t *Table) MCPCall(ctx context.Context, body []byte) []byte {
	reply := func(v map[string]any) []byte { raw, _ := json.Marshal(v); return raw }
	var req struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return reply(map[string]any{"error": "request does not decode"})
	}
	name := ""
	for _, o := range t.Ops {
		if t.ToolName(o) == req.Name {
			name = o.Name
		}
	}
	if name == "" {
		return reply(map[string]any{"error": "unknown tool " + req.Name})
	}
	input, confirm, err := splitConfirm(req.Arguments)
	if err != nil {
		return reply(map[string]any{"error": err.Error()})
	}
	result, err := t.Call(ctx, name, input, confirm)
	if err != nil {
		return reply(map[string]any{"error": err.Error()})
	}
	return reply(map[string]any{"result": result})
}

// ---- HTTP ---------------------------------------------------------------

// Handler mounts GET /api/<noun>/<verb> for read ops (query parameters as
// input) and POST /api/<noun>/<verb> for every op (JSON body, "confirm" in
// the body for writes).
func (t *Table) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		noun, verb, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
		op, ok := t.find(noun + "." + strings.TrimSuffix(verb, "/"))
		if !ok || !strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		var input json.RawMessage
		confirm := false
		var err error
		switch {
		case r.Method == http.MethodGet && op.Risk == Read:
			values := map[string]string{}
			for k, v := range r.URL.Query() {
				values[k] = v[len(v)-1]
			}
			input, err = fromStrings(op, values)
		case r.Method == http.MethodPost:
			var body []byte
			body, err = io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err == nil {
				input, confirm, err = splitConfirm(body)
			}
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST for " + string(op.Risk) + " operations"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		result, err := t.Call(r.Context(), op.Name, input, confirm)
		switch {
		case errors.Is(err, ErrWritesDisabled):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrNeedsConfirm):
			writeJSON(w, http.StatusPreconditionRequired, map[string]string{"error": err.Error()})
		case err != nil:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusOK, result)
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- CLI ----------------------------------------------------------------

// RunCLI runs `<noun> <verb> [--json] [--confirm] [--input '{...}' | --key=value ...]`
// and returns the process exit code: 0 success, 1 operation failed, 2 usage.
// Output is indented JSON, or one compact line with --json.
func (t *Table) RunCLI(ctx context.Context, bin string, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || strings.HasPrefix(args[0], "-") {
		t.Usage(bin, stderr)
		return 2
	}
	op, ok := t.find(args[0] + "." + args[1])
	if !ok {
		fmt.Fprintf(stderr, "%s: unknown command %q\n\n", bin, args[0]+" "+args[1])
		t.Usage(bin, stderr)
		return 2
	}
	compact, confirm := false, false
	var inputJSON string
	values := map[string]string{}
	for _, a := range args[2:] {
		k, v, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch {
		case !strings.HasPrefix(a, "--"):
			fmt.Fprintf(stderr, "%s: unexpected argument %q (use --key=value)\n", bin, a)
			return 2
		case k == "json" && !hasValue:
			compact = true
		case k == "confirm" && !hasValue:
			confirm = true
		case k == "input" && hasValue:
			inputJSON = v
		case hasValue:
			values[k] = v
		default:
			values[k] = "true" // bare --flag is a boolean
		}
	}
	var input json.RawMessage
	var err error
	if inputJSON != "" {
		if len(values) > 0 {
			fmt.Fprintf(stderr, "%s: use --input or --key=value flags, not both\n", bin)
			return 2
		}
		input, _, err = splitConfirm(json.RawMessage(inputJSON))
	} else {
		input, err = fromStrings(op, values)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", bin, err)
		return 2
	}
	result, err := t.Call(ctx, op.Name, input, confirm)
	if errors.Is(err, ErrNeedsConfirm) {
		err = errors.New("this operation changes state; repeat it with --confirm")
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s %s %s: %v\n", bin, args[0], args[1], err)
		return 1
	}
	enc := json.NewEncoder(stdout)
	if !compact {
		enc.SetIndent("", "  ")
	}
	_ = enc.Encode(result)
	return 0
}

// Usage lists every command with its flags and risk tier.
func (t *Table) Usage(bin string, w io.Writer) {
	fmt.Fprintf(w, "Usage: %s <noun> <verb> [--json] [--confirm] [--input '{...}' | --key=value ...]\n\nCommands:\n", bin)
	ops := append([]Op(nil), t.Ops...)
	sort.Slice(ops, func(i, j int) bool { return ops[i].Name < ops[j].Name })
	for _, o := range ops {
		var flags []string
		for _, f := range fields(o.Input) {
			s := "--" + f.name
			if !f.required {
				s = "[" + s + "]"
			}
			flags = append(flags, s)
		}
		risk := ""
		if o.Risk != Read {
			risk = " [" + string(o.Risk) + ", needs --confirm]"
		}
		fmt.Fprintf(w, "  %s %s %s\n      %s%s\n", o.noun(), o.verb(), strings.Join(flags, " "), o.Summary, risk)
	}
}
