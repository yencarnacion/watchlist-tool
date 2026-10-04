package marketgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGatewayBoundary(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer private-test-token" {
			t.Error("missing token")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer s.Close()
	c, e := New(s.URL+"/api/marketdata/v1", "private-test-token")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	for _, p := range []string{"https://example.com/rest/x", "//example.com/x", "/api/marketdata/v1/rest/../x", "/outside/rest/x", "/rest/x?apiKey=bad"} {
		var v any
		if c.Get(context.Background(), p, &v) == nil {
			t.Fatalf("unsafe path allowed: %s", p)
		}
	}
	for _, p := range []string{"/rest/v3/trades/TEST", "/api/marketdata/v1/rest/v3/trades/TEST?cursor=abc"} {
		var v any
		if e = c.Get(context.Background(), p, &v); e != nil {
			t.Fatal(e)
		}
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	for _, u := range []string{"https://api.massive.com", "https://x.polygon.io", "http://example.com/api", "http://user:pass@127.0.0.1/api", "http://127.0.0.1/api?token=x"} {
		if Validate(u) == nil {
			t.Fatalf("bad base allowed %s", u)
		}
	}
}
func TestRedirectDoesNotLeakToken(t *testing.T) {
	var leaked atomic.Bool
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer dest.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, 302) }))
	defer s.Close()
	c, _ := New(s.URL+"/api", "secret")
	defer c.Close()
	var v any
	e := c.Get(context.Background(), "/rest/x", &v)
	if e == nil || leaked.Load() || strings.Contains(e.Error(), "secret") {
		t.Fatal(e)
	}
}
func TestStreamRejectsGapAndPreservesIntegerTimestamp(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: hello\ndata: {\"api_version\":1,\"source\":\"massive\",\"feed\":\"realtime\"}\n\nevent: market\ndata: {\"event_ms\":1790000000123,\"data\":{\"t\":1790000000123456789}}\n\nevent: gap\ndata: {}\n\n")
	}))
	defer s.Close()
	c, _ := New(s.URL+"/api", "")
	defer c.Close()
	count := 0
	e := c.Stream(context.Background(), "TEST", "Q", func(name string, d json.RawMessage) error {
		count++
		if name == "market" {
			var v struct {
				EventMS int64 `json:"event_ms"`
				Data    struct {
					T int64 `json:"t"`
				}
			}
			_ = json.Unmarshal(d, &v)
			if v.EventMS != 1790000000123 || v.Data.T != 1790000000123456789 {
				t.Fatal(v)
			}
		}
		return nil
	})
	if e == nil || !strings.Contains(e.Error(), "gap") || count != 2 {
		t.Fatal(e, count)
	}
}
func TestBarsExcludesFutureAndUsesUnadjusted(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("adjusted") != "false" {
			t.Error("not unadjusted")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"t":1000,"c":1},{"t":2000,"c":2},{"t":3000,"c":3}]}`)
	}))
	defer s.Close()
	c, _ := New(s.URL+"/api", "")
	defer c.Close()
	bs, e := c.Bars(context.Background(), "TEST", "minute", time.UnixMilli(1000), time.UnixMilli(3000))
	if e != nil || len(bs) != 2 {
		t.Fatal(bs, e)
	}
}
