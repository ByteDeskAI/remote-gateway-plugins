// Package mcpclient provides a bounded remote MCP session for connector plugins.
// It owns protocol transport, not credentials, project authority, or domain retries.
package mcpclient

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config is supplied for one acting user's authorized connection. HTTPClient
// carries the host's credential/egress policy; this package never reads secrets.
type Config struct {
	Endpoint          string
	Name              string
	Version           string
	HTTPClient        *http.Client
	AllowLoopbackHTTP bool
	CallTimeout       time.Duration
	MaxTools          int
	MaxPages          int
}

// Error deliberately excludes remote bodies, credentials and MCP session IDs.
// Cause remains available for typed local handling; do not log its raw value.
type Error struct {
	Operation      string
	OutcomeUnknown bool
	cause          error
}

func (e *Error) Error() string {
	if e.OutcomeUnknown {
		return "MCP " + e.Operation + " failed; remote outcome is unknown"
	}
	return "MCP " + e.Operation + " failed"
}
func (e *Error) Unwrap() error { return e.cause }

var (
	ErrToolFailed   = errors.New("MCP tool reported an error")
	ErrNeedsInput   = errors.New("MCP tool requires a supported user interaction")
	ErrInvalidTools = errors.New("MCP tool catalogue is invalid or exceeds limits")
	ErrResponseSize = errors.New("MCP response exceeds the size limit")
)

type Client struct {
	session *mcp.ClientSession
	timeout time.Duration
	tools   int
	pages   int
}

// Connect initializes the official MCP Streamable HTTP transport. It does not
// fall back to REST, launch a process, or follow redirects to another endpoint.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("MCP endpoint must be an absolute URL without credentials, query or fragment")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && cfg.AllowLoopbackHTTP && loopback(u.Hostname())) {
		return nil, errors.New("MCP endpoint requires HTTPS; explicitly enabled loopback HTTP is allowed for local environments")
	}
	if cfg.HTTPClient == nil || strings.TrimSpace(cfg.Name) == "" || strings.TrimSpace(cfg.Version) == "" {
		return nil, errors.New("MCP connection requires a host HTTP client and client identity")
	}
	if cfg.CallTimeout < 0 || cfg.MaxTools < 0 || cfg.MaxPages < 0 {
		return nil, errors.New("MCP limits cannot be negative")
	}
	if cfg.CallTimeout == 0 {
		cfg.CallTimeout = 30 * time.Second
	}
	if cfg.MaxTools == 0 {
		cfg.MaxTools = 10000
	}
	if cfg.MaxPages == 0 {
		cfg.MaxPages = 256
	}
	httpClient := *cfg.HTTPClient
	if httpClient.Timeout == 0 || httpClient.Timeout > cfg.CallTimeout {
		httpClient.Timeout = cfg.CallTimeout
	}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = boundedTransport{base: base}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// No browser cookies or ambient session are shared with the MCP endpoint.
	httpClient.Jar = nil
	client := mcp.NewClient(&mcp.Implementation{Name: cfg.Name, Version: cfg.Version}, &mcp.ClientOptions{
		Capabilities:   &mcp.ClientCapabilities{},
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	callCtx, cancel := context.WithTimeout(ctx, cfg.CallTimeout)
	defer cancel()
	// The SDK may close a failed legacy handshake with a detached DELETE.
	// Keep that cleanup inside the original deadline as well as initialization.
	handshake := &handshakeTransport{base: httpClient.Transport, ctx: callCtx}
	handshake.active.Store(true)
	httpClient.Transport = handshake
	defer handshake.active.Store(false)
	session, err := client.Connect(callCtx, &mcp.StreamableClientTransport{
		Endpoint: cfg.Endpoint, HTTPClient: &httpClient,
		// Domain recovery belongs to the provider's durable operation receipts.
		// Disable failed reconnect retries and the standalone server listener.
		// The SDK may resume an interrupted response with GET; it does not replay
		// tools/call, and automatic multi-round-trip tool calls are disabled above.
		MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: 8 << 20,
	}, nil)
	if callCtx.Err() != nil {
		if session != nil {
			_ = session.Close()
		}
		err = callCtx.Err()
	}
	if err != nil {
		return nil, &Error{Operation: "connect", cause: err}
	}
	return &Client{session: session, timeout: cfg.CallTimeout, tools: cfg.MaxTools, pages: cfg.MaxPages}, nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Bound both JSON and streamed responses, including responses without a length.
// Large attachments use a separately authorized transfer, not inline tool bytes.
const maxResponseBytes int64 = 8 << 20

type boundedTransport struct{ base http.RoundTripper }

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if res.ContentLength > maxResponseBytes {
		_ = res.Body.Close()
		return nil, ErrResponseSize
	}
	res.Body = &boundedBody{ReadCloser: res.Body, remaining: maxResponseBytes}
	return res, nil
}

type handshakeTransport struct {
	base   http.RoundTripper
	ctx    context.Context
	active atomic.Bool
}

func (t *handshakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !t.active.Load() {
		return t.base.RoundTrip(r)
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(t.ctx, cancel)
	cleanup := func() { stop(); cancel() }
	if err := t.ctx.Err(); err != nil {
		cleanup()
		return nil, err
	}
	res, err := t.base.RoundTrip(r.Clone(ctx))
	if err != nil {
		cleanup()
		return nil, err
	}
	res.Body = &cleanupBody{ReadCloser: res.Body, cleanup: cleanup}
	return res, nil
}

type cleanupBody struct {
	io.ReadCloser
	cleanup func()
	once    sync.Once
}

func (b *cleanupBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.cleanup)
	}
	return n, err
}

func (b *cleanupBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.cleanup)
	return err
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	if int64(n) > b.remaining {
		b.remaining = 0
		return 0, ErrResponseSize
	}
	b.remaining -= int64(n)
	return n, err
}

func (c *Client) Close() error {
	if err := c.session.Close(); err != nil {
		return &Error{Operation: "close", cause: err}
	}
	return nil
}

// Tools reads every catalogue page. Deferred vendor tools still need the
// provider's explicit discovery mapping; this is not a fixed tool allowlist.
func (c *Client) Tools(ctx context.Context) ([]*mcp.Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var tools []*mcp.Tool
	cursor := ""
	cursors := map[string]bool{"": true}
	names := map[string]bool{}
	for page := 0; page < c.pages; page++ {
		result, err := c.session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, &Error{Operation: "list tools", cause: err}
		}
		if result == nil || len(result.Tools) > c.tools-len(tools) {
			return nil, ErrInvalidTools
		}
		for _, tool := range result.Tools {
			if tool == nil || strings.TrimSpace(tool.Name) == "" || names[tool.Name] {
				return nil, ErrInvalidTools
			}
			names[tool.Name] = true
			tools = append(tools, tool)
		}
		cursor = result.NextCursor
		if cursor == "" {
			return tools, nil
		}
		if cursors[cursor] {
			return nil, ErrInvalidTools
		}
		cursors[cursor] = true
	}
	return nil, ErrInvalidTools
}

// Call executes once. A failed mutation has an unknown outcome unless the
// provider can recover its durable receipt; never blindly replay it. Results
// with tool errors or input requests remain available for structured mapping.
func (c *Client) Call(ctx context.Context, name string, arguments any, mutation bool) (*mcp.CallToolResult, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("MCP tool name is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, &Error{Operation: "call tool", cause: err}
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	result, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, &Error{Operation: "call tool", OutcomeUnknown: mutation, cause: err}
	}
	if result == nil {
		return nil, &Error{Operation: "call tool", OutcomeUnknown: mutation, cause: errors.New("empty tool result")}
	}
	if result.NeedsInput() {
		return result, ErrNeedsInput
	}
	if result.IsError {
		return result, ErrToolFailed
	}
	return result, nil
}
