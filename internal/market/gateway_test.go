package market

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
	"watchlist-tool/internal/config"
)

func testLocation(t *testing.T) *time.Location {
	t.Helper()
	loc, e := time.LoadLocation("America/New_York")
	if e != nil {
		t.Fatal(e)
	}
	return loc
}
func testEvent(symbol, channel string, start int64, close, volume, av float64) sourceEvent {
	b, _ := json.Marshal(map[string]any{"s": start, "e": start + 999, "c": close, "v": volume, "av": av})
	return sourceEvent{Symbol: symbol, Channel: channel, EventMS: start + 999, ReceivedMS: time.Now().UnixMilli(), Data: b}
}
func TestProviderTimestampsAndNoClockRefreshFromQuotes(t *testing.T) {
	loc := testLocation(t)
	h := NewHub(1000)
	h.Symbols([]string{"AAPL"})
	at := time.Date(2026, 9, 8, 10, 0, 0, 0, loc).UnixMilli()
	var snap stockSnapshot
	snap.Ticker.LastTrade.P = 100
	snap.Ticker.LastTrade.T = at * 1000000
	snap.Ticker.PrevDay.C = 90
	h.mergeSourceHistory("AAPL", []Point{{T: at, P: 100, V: 50}}, snap, 0, "realtime", "", loc)
	q, _ := h.Snapshot()
	if q["AAPL"].Updated != at {
		t.Fatal("snapshot nanoseconds were not normalized")
	}
	h.applySourceEvent(sourceEvent{Symbol: "AAPL", Channel: "Q", EventMS: at + 10000, Data: json.RawMessage(`{"bp":99,"ap":101}`)}, "realtime", loc)
	q, _ = h.Snapshot()
	if q["AAPL"].Updated != at {
		t.Fatal("quote made last-sale clock fresh")
	}
	h.mergeSourceHistory("AAPL", nil, snap, time.Now().UnixNano(), "realtime", "", loc)
	q, _ = h.Snapshot()
	if q["AAPL"].Updated != at {
		t.Fatal("receiving old REST data made the price fresh")
	}
}
func TestLateBackfillAndMinuteRevisionDoNotDoubleCount(t *testing.T) {
	loc := testLocation(t)
	h := NewHub(1000)
	h.Symbols([]string{"X"})
	at := time.Date(2026, 9, 8, 10, 0, 0, 0, loc).UnixMilli()
	started := time.Now().UnixNano()
	h.applySourceEvent(testEvent("X", "AM", at, 100, 50, 500), "realtime", loc)
	h.applySourceEvent(testEvent("X", "AM", at, 101, 75, 525), "realtime", loc)
	h.mergeSourceHistory("X", []Point{{T: at, P: 99, V: 25}}, stockSnapshot{}, started, "realtime", "", loc)
	q, _ := h.Snapshot()
	v := q["X"]
	if len(v.History) != 1 || v.History[0].V != 75 || v.History[0].P != 101 || v.Volume != 525 {
		t.Fatalf("late backfill overwrote live state: %+v", v)
	}
	h.applySourceEvent(testEvent("X", "A", at-60000, 90, 10, 100), "realtime", loc)
	q, _ = h.Snapshot()
	if q["X"].Last != 101 || q["X"].Volume != 525 {
		t.Fatal("out-of-order event regressed last or volume")
	}
}
func TestNewSessionResetsOldHistoryAndBenchmark(t *testing.T) {
	loc := testLocation(t)
	h := NewHub(1000)
	h.Symbols([]string{"X"})
	at := time.Date(2026, 9, 8, 19, 59, 0, 0, loc).UnixMilli()
	h.applySourceEvent(testEvent("X", "AM", at, 100, 20, 2000), "realtime", loc)
	next := time.Date(2026, 9, 9, 4, 0, 0, 0, loc).UnixMilli()
	h.applySourceEvent(testEvent("X", "A", next, 110, 5, 5), "realtime", loc)
	h.applySourceEvent(testEvent("X", "AM", at, 101, 30, 2100), "realtime", loc)
	q, _ := h.Snapshot()
	if len(q["X"].History) != 1 || q["X"].Last != 110 || q["X"].PrevClose != 0 || q["X"].Volume != 5 {
		t.Fatalf("day rollover mixed sessions: %+v", q["X"])
	}
}
func TestClientUsesOnlyGatewayAndReconnectsForSymbolChanges(t *testing.T) {
	var streams, rest atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer local-test-token" {
			t.Error("missing gateway token")
		}
		if !strings.HasPrefix(r.URL.Path, "/api/marketdata/v1/") {
			t.Error("not a gateway path")
		}
		if strings.HasSuffix(r.URL.Path, "/stream") {
			streams.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: hello\ndata: {\"api_version\":1,\"source\":\"massive\",\"feed\":\"delayed\"}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		rest.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[],"ticker":{}}`)
	}))
	defer srv.Close()
	t.Setenv("TEST_GATEWAY_TOKEN", "local-test-token")
	var cfg config.Config
	cfg.App.Timezone = "America/New_York"
	cfg.Gateway.APIURL = srv.URL + "/api/marketdata/v1"
	cfg.Gateway.Token = "local-test-token"
	cfg.Gateway.Channels = "A,AM"
	cfg.Gateway.ReconnectInterval = "1s"
	cfg.Gateway.RefreshInterval = "60s"
	h := NewHub(1000)
	f := NewGateway(cfg, h)
	f.SetSymbols([]string{"NOTINSCANNER"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); f.Run(ctx) }()
	await := func(fn func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !fn() {
			if time.Now().After(deadline) {
				cancel()
				t.Fatal("timed out")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	await(func() bool { return streams.Load() >= 1 && rest.Load() >= 2 })
	f.SetSymbols([]string{"BRK.B"})
	await(func() bool { return streams.Load() >= 2 })
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("client did not stop promptly")
	}
	q, status := h.Snapshot()
	if _, ok := q["NOTINSCANNER"]; ok {
		t.Fatal("removed symbol cache leaked")
	}
	if !strings.Contains(status.Message, "delayed") {
		t.Fatal("delayed feed not disclosed")
	}
}
