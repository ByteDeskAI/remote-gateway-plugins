// Package kit turns one table of operations into a plugin's MCP tools, HTTP
// routes and CLI. Every surface goes through Table.Call, so the write gate is
// enforced in exactly one place.
package kit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Risk is an operation's tier. Read runs freely; Write and Destructive need
// writes enabled AND an explicit confirm on every call.
type Risk string

const (
	Read        Risk = "read"
	Write       Risk = "write"
	Destructive Risk = "destructive"
)

// Op is one operation. Name is "noun.verb". Input is a zero value of the
// struct the handler decodes (nil for no input); its json tags drive the
// schema, CLI flags and GET query parsing. Tag a field `kit:"required"` and
// describe it with `desc:"..."`.
type Op struct {
	Name    string
	Summary string
	Risk    Risk
	Input   any
	Handle  func(ctx context.Context, input json.RawMessage) (any, error)
}

func (o Op) noun() string { n, _, _ := strings.Cut(o.Name, "."); return n }
func (o Op) verb() string { _, v, _ := strings.Cut(o.Name, "."); return v }

// Table is the plugin's operation set.
type Table struct {
	// Prefix names MCP tools: <prefix>_<noun>_<verb>.
	Prefix string
	Ops    []Op
	// WritesEnabled is read on every write call. Nil means writes are off.
	WritesEnabled func() bool
}

var (
	ErrUnknownOp      = errors.New("unknown operation")
	ErrNeedsConfirm   = errors.New("this operation changes state; repeat it with confirm: true")
	ErrWritesDisabled = errors.New("write operations are disabled; turn on write tools in the plugin settings")
)

func (t *Table) find(name string) (Op, bool) {
	for _, o := range t.Ops {
		if o.Name == name {
			return o, true
		}
	}
	return Op{}, false
}

// Call runs one operation through the write gate.
func (t *Table) Call(ctx context.Context, name string, input json.RawMessage, confirm bool) (any, error) {
	op, ok := t.find(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownOp, name)
	}
	if op.Risk != Read {
		if t.WritesEnabled == nil || !t.WritesEnabled() {
			return nil, ErrWritesDisabled
		}
		if !confirm {
			return nil, ErrNeedsConfirm
		}
	}
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	return op.Handle(ctx, input)
}

// field is one input property derived from a struct's json tag.
type field struct {
	name     string
	kind     reflect.Kind
	required bool
	desc     string
}

func fields(input any) []field {
	if input == nil {
		return nil
	}
	rt := reflect.TypeOf(input)
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	var out []field
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, field{name: name, kind: f.Type.Kind(), required: f.Tag.Get("kit") == "required", desc: f.Tag.Get("desc")})
	}
	return out
}

func jsonType(k reflect.Kind) string {
	switch k {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	default:
		return "object"
	}
}

// Schema is the JSON schema of an op's input. Write ops gain "confirm".
func (o Op) Schema() map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, f := range fields(o.Input) {
		p := map[string]any{"type": jsonType(f.kind)}
		if f.desc != "" {
			p["description"] = f.desc
		}
		props[f.name] = p
		if f.required {
			required = append(required, f.name)
		}
	}
	if o.Risk != Read {
		props["confirm"] = map[string]any{"type": "boolean", "description": "Must be true. Confirms this " + string(o.Risk) + " operation."}
		required = append(required, "confirm")
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// fromStrings builds an op's JSON input from string values (CLI flags, query
// parameters), converting by the struct field's kind. Object and array fields
// take JSON text.
func fromStrings(o Op, values map[string]string) (json.RawMessage, error) {
	known := map[string]field{}
	for _, f := range fields(o.Input) {
		known[f.name] = f
	}
	obj := map[string]any{}
	for k, v := range values {
		f, ok := known[k]
		if !ok {
			return nil, fmt.Errorf("unknown parameter %q for %s", k, o.Name)
		}
		switch jsonType(f.kind) {
		case "string":
			obj[k] = v
		case "boolean":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("%s: want true or false", k)
			}
			obj[k] = b
		case "integer", "number":
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: want a number", k)
			}
			obj[k] = n
		default:
			var raw json.RawMessage
			if err := json.Unmarshal([]byte(v), &raw); err != nil {
				return nil, fmt.Errorf("%s: want JSON: %v", k, err)
			}
			obj[k] = raw
		}
	}
	return json.Marshal(obj)
}

// splitConfirm removes "confirm" from a JSON object input and reports it.
func splitConfirm(input json.RawMessage) (json.RawMessage, bool, error) {
	if len(input) == 0 {
		return nil, false, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(input, &obj); err != nil {
		return nil, false, fmt.Errorf("input must be a JSON object: %w", err)
	}
	confirm := string(obj["confirm"]) == "true"
	delete(obj, "confirm")
	out, err := json.Marshal(obj)
	return out, confirm, err
}
