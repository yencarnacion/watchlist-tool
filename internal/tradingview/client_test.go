package tradingview

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeConnection struct {
	written []byte
	reads   []any
	closed  bool
}

func (f *fakeConnection) WriteJSON(v any) error {
	var err error
	f.written, err = json.Marshal(v)
	return err
}

func (f *fakeConnection) ReadJSON(v any) error {
	if len(f.reads) == 0 {
		return io.EOF
	}
	value := f.reads[0]
	f.reads = f.reads[1:]
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func (f *fakeConnection) SetReadDeadline(time.Time) error  { return nil }
func (f *fakeConnection) SetWriteDeadline(time.Time) error { return nil }
func (f *fakeConnection) SetReadLimit(int64)               {}
func (f *fakeConnection) Close() error {
	f.closed = true
	return nil
}

func TestParseLoopbackHTTPURL(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:9222", "http://localhost:9222", "http://[::1]:9222"} {
		if _, err := parseLoopbackHTTPURL(raw); err != nil {
			t.Fatalf("parseLoopbackHTTPURL(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"https://example.com:9222", "http://0.0.0.0:9222", "file:///tmp/cdp"} {
		if _, err := parseLoopbackHTTPURL(raw); err == nil {
			t.Fatalf("parseLoopbackHTTPURL(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestSelectChartTargetPrefersChartURL(t *testing.T) {
	targets := []cdpTarget{
		{Type: "page", Title: "TradingView", URL: "https://www.tradingview.com/", WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/fallback"},
		{Type: "page", URL: "https://www.tradingview.com/chart/abc/", WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/chart"},
	}
	got, ok := selectChartTarget(targets)
	if !ok || !strings.HasSuffix(got.WebSocketDebuggerURL, "/chart") {
		t.Fatalf("target = %#v, ok = %v", got, ok)
	}
}

func TestSelectChartTargetRejectsRemoteWebSocket(t *testing.T) {
	_, ok := selectChartTarget([]cdpTarget{{
		Type: "page", URL: "https://www.tradingview.com/chart/abc/", WebSocketDebuggerURL: "ws://192.0.2.10:9222/devtools/page/chart",
	}})
	if ok {
		t.Fatal("accepted non-loopback CDP WebSocket target")
	}
}

func TestSetSymbolExpressionEscapesInput(t *testing.T) {
	expression := setSymbolExpression(`AAPL"; throw new Error("bad")`)
	if strings.Contains(expression, `chart.setSymbol(AAPL`) {
		t.Fatalf("symbol was not quoted: %s", expression)
	}
	if !strings.Contains(expression, `chart.setSymbol("AAPL\"; throw new Error(\"bad\")"`) {
		t.Fatalf("escaped symbol not found: %s", expression)
	}
}

func TestSetSymbolSendsRuntimeEvaluate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/list" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]cdpTarget{{
			Type: "page", URL: "https://www.tradingview.com/chart/test/", WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/1",
		}})
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection := &fakeConnection{reads: []any{
		map[string]any{"method": "Runtime.consoleAPICalled"},
		map[string]any{"id": 1, "result": map[string]any{"result": map[string]any{"value": map[string]any{"current": "NASDAQ:AAPL"}}}},
	}}
	client := &Client{
		enabled: true,
		cdpURL:  parsed,
		timeout: time.Second,
		http:    server.Client(),
		dial: func(context.Context, string, http.Header) (cdpConnection, *http.Response, error) {
			return connection, nil, nil
		},
	}

	if err := client.SetSymbol(context.Background(), "aapl"); err != nil {
		t.Fatal(err)
	}
	if !connection.closed {
		t.Fatal("connection was not closed")
	}
	var request struct {
		ID     int            `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(connection.written, &request); err != nil {
		t.Fatal(err)
	}
	if request.ID != 1 || request.Method != "Runtime.evaluate" {
		t.Fatalf("request = %#v", request)
	}
	expression, _ := request.Params["expression"].(string)
	if !strings.Contains(expression, `chart.setSymbol("AAPL", {})`) {
		t.Fatalf("expression = %s", expression)
	}
}

func TestSetSymbolReturnsCDPException(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]cdpTarget{{
			Type: "page", URL: "https://www.tradingview.com/chart/test/", WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/1",
		}})
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	connection := &fakeConnection{reads: []any{map[string]any{
		"id": 1,
		"result": map[string]any{"exceptionDetails": map[string]any{
			"text": "Uncaught", "exception": map[string]any{"description": "TradingView chart API is unavailable"},
		}},
	}}}
	client := &Client{
		enabled: true, cdpURL: parsed, timeout: time.Second, http: server.Client(),
		dial: func(context.Context, string, http.Header) (cdpConnection, *http.Response, error) {
			return connection, nil, nil
		},
	}

	err := client.SetSymbol(context.Background(), "AAPL")
	if err == nil || !strings.Contains(err.Error(), "chart API is unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestSetSymbolRejectsUnexpectedCurrentSymbol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]cdpTarget{{
			Type: "page", URL: "https://www.tradingview.com/chart/test/", WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/1",
		}})
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	connection := &fakeConnection{reads: []any{map[string]any{
		"id": 1, "result": map[string]any{"result": map[string]any{"value": map[string]any{"current": "NASDAQ:MSFT"}}},
	}}}
	client := &Client{
		enabled: true, cdpURL: parsed, timeout: time.Second, http: server.Client(),
		dial: func(context.Context, string, http.Header) (cdpConnection, *http.Response, error) {
			return connection, nil, nil
		},
	}
	if err := client.SetSymbol(context.Background(), "AAPL"); err == nil || !strings.Contains(err.Error(), "MSFT") {
		t.Fatalf("error = %v", err)
	}
}

func TestDisabledClient(t *testing.T) {
	client := &Client{}
	if client.Status(context.Background()).Enabled {
		t.Fatal("disabled client reported enabled")
	}
	if err := client.SetSymbol(context.Background(), "AAPL"); err == nil {
		t.Fatal("disabled SetSymbol succeeded")
	}
}

func TestStatusReportsTargetFailure(t *testing.T) {
	parsed, _ := url.Parse("http://127.0.0.1:1")
	client := &Client{
		enabled: true,
		cdpURL:  parsed,
		timeout: 20 * time.Millisecond,
		http:    &http.Client{Timeout: 20 * time.Millisecond},
		dial: func(context.Context, string, http.Header) (cdpConnection, *http.Response, error) {
			return nil, nil, errors.New("unexpected dial")
		},
	}
	status := client.Status(context.Background())
	if status.Connected || status.Error == "" {
		t.Fatalf("status = %#v", status)
	}
}
