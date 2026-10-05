package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectTest(t *testing.T, handler http.Handler, adjust func(*Config)) *Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	cfg := Config{Endpoint: s.URL, HTTPClient: s.Client(), Name: "connector-test", Version: "1", AllowLoopbackHTTP: true, CallTimeout: 2 * time.Second}
	if adjust != nil {
		adjust(&cfg)
	}
	c, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func sdkHandler(jsonResponse bool) http.Handler {
	return sdkHandlerVersion(jsonResponse, "")
}

func sdkHandlerVersion(jsonResponse bool, version string) http.Handler {
	opts := &mcp.ServerOptions{PageSize: 1}
	if version != "" {
		opts.SupportedProtocolVersions = []string{version}
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, opts)
	s.AddTool(&mcp.Tool{Name: "get", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{StructuredContent: map[string]any{"title": "shared work"}}, nil
	})
	s.AddTool(&mcp.Tool{Name: "reject", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "private provider failure"}}}, nil
	})
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{JSONResponse: jsonResponse})
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		ProtocolVersion string `json:"protocolVersion"`
	} `json:"params"`
}

// Inspect the wire request without changing the official server's response or
// replacing ResponseWriter's streaming/flush capabilities.
func inspectRPC(t *testing.T, r *http.Request) rpcMessage {
	t.Helper()
	var message rpcMessage
	if r.Method != http.MethodPost {
		return message
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Error(err)
		return message
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if err := json.Unmarshal(body, &message); err != nil {
		t.Error(err)
	}
	return message
}

func TestLegacyProtocolJSONAndSSE(t *testing.T) {
	const version = "2025-11-25"
	for _, jsonResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "SSE", true: "JSON"}[jsonResponse], func(t *testing.T) {
			type observation struct{ method, protocol, initializeVersion, contentType string }
			observed := make(chan observation, 16)
			base := sdkHandlerVersion(jsonResponse, version)
			c := connectTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				message := inspectRPC(t, r)
				base.ServeHTTP(w, r)
				if message.Method != "" {
					observed <- observation{message.Method, r.Header.Get("Mcp-Protocol-Version"), message.Params.ProtocolVersion, w.Header().Get("Content-Type")}
				}
			}), nil)
			tools, err := c.Tools(context.Background())
			if err != nil || len(tools) != 2 {
				t.Fatalf("legacy catalogue len=%d err=%v", len(tools), err)
			}
			if _, err := c.Call(context.Background(), "get", map[string]any{}, false); err != nil {
				t.Fatal(err)
			}
			wantType := "text/event-stream"
			if jsonResponse {
				wantType = "application/json"
			}
			initialized, lists, calls := false, 0, 0
			deadline := time.After(time.Second)
			for !initialized || lists != 2 || calls != 1 {
				select {
				case got := <-observed:
					switch got.method {
					case "initialize":
						initialized = true
						if got.initializeVersion != version {
							t.Fatalf("initialize requested %q; want %s", got.initializeVersion, version)
						}
					case "tools/list", "tools/call":
						if got.protocol != version || !strings.HasPrefix(got.contentType, wantType) {
							t.Fatalf("wire observation %+v; want protocol %s and content type %s", got, version, wantType)
						}
						if got.method == "tools/list" {
							lists++
						} else {
							calls++
						}
					}
				case <-deadline:
					t.Fatalf("missing wire observations: initialized=%v lists=%d calls=%d", initialized, lists, calls)
				}
			}
		})
	}
}

func TestMalformedAndLoopingCatalogues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result func(int) any
		calls  int32
	}{
		{"repeated cursor", func(page int) any {
			return map[string]any{"tools": []any{map[string]any{"name": []string{"first", "second"}[page-1], "inputSchema": map[string]any{"type": "object"}}}, "nextCursor": "same"}
		}, 2},
		{"duplicate name", func(page int) any {
			return map[string]any{"tools": []any{map[string]any{"name": "duplicate", "inputSchema": map[string]any{"type": "object"}}}, "nextCursor": []string{"next", ""}[page-1]}
		}, 2},
		{"empty name", func(int) any {
			return map[string]any{"tools": []any{map[string]any{"name": " ", "inputSchema": map[string]any{"type": "object"}}}}
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			base := sdkHandlerVersion(true, "2025-11-25")
			c := connectTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				message := inspectRPC(t, r)
				if message.Method == "tools/list" {
					page := calls.Add(1)
					if page > tc.calls {
						t.Errorf("unexpected catalogue request %d", page)
						http.Error(w, "too many requests", http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": tc.result(int(page))})
					return
				}
				base.ServeHTTP(w, r)
			}), nil)
			if _, err := c.Tools(context.Background()); !errors.Is(err, ErrInvalidTools) || calls.Load() != tc.calls {
				t.Fatalf("err=%v calls=%d; want invalid catalogue after %d requests", err, calls.Load(), tc.calls)
			}
		})
	}
}

func TestMalformedMutationResultHasUnknownOutcome(t *testing.T) {
	var calls atomic.Int32
	base := sdkHandler(true)
	c := connectTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		message := inspectRPC(t, r)
		if message.Method == "tools/call" {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": "private malformed result"})
			return
		}
		base.ServeHTTP(w, r)
	}), nil)
	result, err := c.Call(context.Background(), "get", map[string]any{}, true)
	var failed *Error
	if result != nil || !errors.As(err, &failed) || !failed.OutcomeUnknown || strings.Contains(err.Error(), "private") || calls.Load() != 1 {
		t.Fatalf("result=%v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestInputRequiredDoesNotReplayMutation(t *testing.T) {
	var calls atomic.Int32
	base := sdkHandler(true)
	c := connectTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		message := inspectRPC(t, r)
		if message.Method == "tools/call" {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{
				"resultType": "input_required", "inputRequests": map[string]any{}, "requestState": "fixture-state",
			}})
			return
		}
		base.ServeHTTP(w, r)
	}), nil)
	result, err := c.Call(context.Background(), "get", map[string]any{}, true)
	if !errors.Is(err, ErrNeedsInput) || result == nil || !result.NeedsInput() || result.RequestState != "fixture-state" || calls.Load() != 1 {
		t.Fatalf("result=%v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestHostTransportAndSessionOutliveConnectContext(t *testing.T) {
	var authorized atomic.Int32
	base := sdkHandlerVersion(true, "2025-11-25")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-only" {
			t.Error("request bypassed host credential transport")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		authorized.Add(1)
		base.ServeHTTP(w, r)
	}))
	defer s.Close()
	hc := s.Client()
	transport := hc.Transport
	hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer fixture-only")
		return transport.RoundTrip(r)
	})
	ctx, cancel := context.WithCancel(context.Background())
	c, err := Connect(ctx, Config{Endpoint: s.URL, HTTPClient: hc, Name: "test", Version: "1", AllowLoopbackHTTP: true})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Tools(context.Background()); err != nil {
		t.Fatalf("catalogue after handshake cancellation: %v", err)
	}
	if _, err := c.Call(context.Background(), "get", map[string]any{}, false); err != nil {
		t.Fatalf("call after handshake cancellation: %v", err)
	}
	if authorized.Load() < 4 || hc.Timeout != 0 {
		t.Fatalf("host transport requests=%d original client timeout=%s", authorized.Load(), hc.Timeout)
	}
}

func TestRoundTripAndPagination(t *testing.T) {
	for _, jsonResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "SSE", true: "JSON"}[jsonResponse], func(t *testing.T) {
			c := connectTest(t, sdkHandler(jsonResponse), nil)
			tools, err := c.Tools(context.Background())
			if err != nil || len(tools) != 2 {
				t.Fatalf("tools=%v err=%v", tools, err)
			}
			result, err := c.Call(context.Background(), "get", map[string]any{}, false)
			if err != nil || result.StructuredContent.(map[string]any)["title"] != "shared work" {
				t.Fatalf("get result=%v err=%v", result, err)
			}
			result, err = c.Call(context.Background(), "reject", map[string]any{}, true)
			if !errors.Is(err, ErrToolFailed) || result == nil || !result.IsError || strings.Contains(err.Error(), "private") {
				t.Fatalf("tool rejection result=%v err=%v", result, err)
			}
		})
	}
}

func TestCatalogueLimits(t *testing.T) {
	for _, cfg := range []Config{{MaxTools: 1}, {MaxPages: 1}} {
		c := connectTest(t, sdkHandler(true), func(c *Config) { c.MaxTools, c.MaxPages = cfg.MaxTools, cfg.MaxPages })
		if _, err := c.Tools(context.Background()); !errors.Is(err, ErrInvalidTools) {
			t.Fatalf("limits %+v: %v", cfg, err)
		}
	}
}

func TestEndpointRefusalBeforeNetwork(t *testing.T) {
	for _, endpoint := range []string{"/mcp", "ftp://example.test/mcp", "http://example.test/mcp", "https://user:secret@example.test/mcp", "https://example.test/mcp?token=secret", "https://example.test/mcp#secret", "http://127.0.0.1/mcp"} {
		_, err := Connect(context.Background(), Config{Endpoint: endpoint, HTTPClient: http.DefaultClient, Name: "test", Version: "1"})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("endpoint should be rejected safely: %v", err)
		}
	}
}

func TestNoRedirectsOrBrowserCookies(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	var cookies atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			cookies.Add(1)
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	u, _ := url.Parse(s.URL)
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(u, []*http.Cookie{{Name: "browser", Value: "secret"}})
	hc := s.Client()
	hc.Jar = jar
	_, err := Connect(context.Background(), Config{Endpoint: s.URL, HTTPClient: hc, Name: "test", Version: "1", AllowLoopbackHTTP: true})
	if err == nil || redirected.Load() != 0 || cookies.Load() != 0 || hc.Jar != jar {
		t.Fatalf("err=%v redirects=%d cookies=%d; input client must stay unchanged", err, redirected.Load(), cookies.Load())
	}
}

func TestLostMutationResponseIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	base := sdkHandler(true)
	c := connectTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(b)))
			var msg struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(b, &msg)
			if msg.Method == "tools/call" {
				calls.Add(1)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
				return
			}
		}
		base.ServeHTTP(w, r)
	}), nil)
	_, err := c.Call(context.Background(), "get", map[string]any{}, true)
	var failed *Error
	if !errors.As(err, &failed) || !failed.OutcomeUnknown || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestCancelledMutationDoesNotSend(t *testing.T) {
	c := connectTest(t, sdkHandler(true), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Call(ctx, "get", map[string]any{}, true)
	var failed *Error
	if !errors.As(err, &failed) || failed.OutcomeUnknown || !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestProtocolErrorDoesNotExposeProviderBody(t *testing.T) {
	_, err := Connect(context.Background(), Config{Endpoint: "https://example.test/mcp", Name: "test", Version: "1", HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Status: "401 Unauthorized", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private-token-and-session")), Request: r}, nil
	})}})
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestResponseLimitWithAndWithoutContentLength(t *testing.T) {
	for _, length := range []int64{-1, maxResponseBytes + 1} {
		transport := boundedTransport{base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, ContentLength: length, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", int(maxResponseBytes)+1))), Request: r}, nil
		})}
		r, _ := http.NewRequest(http.MethodGet, "https://example.test/mcp", nil)
		res, err := transport.RoundTrip(r)
		if err == nil {
			_, err = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
		}
		if !errors.Is(err, ErrResponseSize) {
			t.Fatalf("length=%d err=%v", length, err)
		}
	}
}

func TestConnectDeadline(t *testing.T) {
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer s.Close()
	defer close(release)
	start := time.Now()
	_, err := Connect(context.Background(), Config{Endpoint: s.URL, HTTPClient: s.Client(), Name: "test", Version: "1", AllowLoopbackHTTP: true, CallTimeout: 50 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("connect deadline err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestConnectDeadlineIncludesFailedSessionCleanup(t *testing.T) {
	release := make(chan struct{})
	base := sdkHandlerVersion(true, "2025-11-25")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		message := inspectRPC(t, r)
		if message.Method == "notifications/initialized" || r.Method == http.MethodDelete {
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		base.ServeHTTP(w, r)
	}))
	defer s.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Connect(ctx, Config{Endpoint: s.URL, HTTPClient: s.Client(), Name: "test", Version: "1", AllowLoopbackHTTP: true, CallTimeout: 500 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 250*time.Millisecond {
		t.Fatalf("connect deadline including cleanup err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestMutationCallDeadline(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	base := sdkHandler(true)
	c := connectTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		message := inspectRPC(t, r)
		if message.Method == "tools/call" {
			calls.Add(1)
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		base.ServeHTTP(w, r)
	}), nil)
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Call(ctx, "get", map[string]any{}, true)
	var failed *Error
	if !errors.As(err, &failed) || !failed.OutcomeUnknown || !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 || time.Since(start) > time.Second {
		t.Fatalf("mutation deadline err=%v calls=%d elapsed=%s", err, calls.Load(), time.Since(start))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
