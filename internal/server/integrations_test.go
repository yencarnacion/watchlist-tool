package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"watchlist-tool/internal/config"
	"watchlist-tool/internal/tradingview"
)

type fakeTradingViewController struct {
	mu      sync.Mutex
	enabled bool
	err     error
	calls   []string
}

func (f *fakeTradingViewController) Enabled() bool { return f.enabled }
func (f *fakeTradingViewController) Status(context.Context) tradingview.Status {
	return tradingview.Status{Enabled: f.enabled, Connected: f.enabled && f.err == nil}
}
func (f *fakeTradingViewController) SetSymbol(_ context.Context, symbol string) error {
	f.mu.Lock()
	f.calls = append(f.calls, symbol)
	f.mu.Unlock()
	return f.err
}

func newSelectionTestServer(t *testing.T, tv tradingViewController, tapeStatus int) (*Server, *string, func()) {
	t.Helper()
	var received string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Symbol string }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode Tape Reading Tool request: %v", err)
		}
		received = input.Symbol
		w.WriteHeader(tapeStatus)
	}))
	var cfg config.Config
	cfg.Integrations.TapeURL = upstream.URL
	return &Server{cfg: cfg, client: upstream.Client(), tv: tv}, &received, upstream.Close
}

func TestSelectTickerSendsToTapeAndTradingView(t *testing.T) {
	tv := &fakeTradingViewController{enabled: true}
	server, tapeSymbol, closeUpstream := newSelectionTestServer(t, tv, http.StatusNoContent)
	defer closeUpstream()

	request := httptest.NewRequest(http.MethodPost, "/api/select", strings.NewReader(`{"symbol":"nvda"}`))
	request.RemoteAddr = "127.0.0.1:41000"
	response := httptest.NewRecorder()
	server.selectTicker(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	var result selectionResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Symbol != "NVDA" || !result.TapeOK || !result.TradingViewEnabled || !result.TradingViewOK {
		t.Fatalf("result = %#v", result)
	}
	if *tapeSymbol != "NVDA" {
		t.Fatalf("Tape Reading Tool received %q", *tapeSymbol)
	}
	if len(tv.calls) != 1 || tv.calls[0] != "NVDA" {
		t.Fatalf("TradingView calls = %#v", tv.calls)
	}
}

func TestSelectTickerKeepsTapeWorkingWhenTradingViewFails(t *testing.T) {
	tv := &fakeTradingViewController{enabled: true, err: errors.New("TradingView offline")}
	server, _, closeUpstream := newSelectionTestServer(t, tv, http.StatusNoContent)
	defer closeUpstream()

	request := httptest.NewRequest(http.MethodPost, "/api/select", strings.NewReader(`{"symbol":"AAPL"}`))
	request.RemoteAddr = "127.0.0.1:41000"
	response := httptest.NewRecorder()
	server.selectTicker(response, request)
	var result selectionResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.TapeOK || result.TradingViewOK || !strings.Contains(result.TradingViewError, "offline") {
		t.Fatalf("result = %#v", result)
	}
}

func TestSelectTickerDoesNotExposeTradingViewToRemoteClients(t *testing.T) {
	tv := &fakeTradingViewController{enabled: true}
	server, _, closeUpstream := newSelectionTestServer(t, tv, http.StatusNoContent)
	defer closeUpstream()

	request := httptest.NewRequest(http.MethodPost, "/api/select", strings.NewReader(`{"symbol":"AAPL"}`))
	request.RemoteAddr = "192.0.2.20:41000"
	response := httptest.NewRecorder()
	server.selectTicker(response, request)
	var result selectionResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.TapeOK || result.TradingViewOK || !strings.Contains(result.TradingViewError, "local requests") {
		t.Fatalf("result = %#v", result)
	}
	if len(tv.calls) != 0 {
		t.Fatalf("remote request reached TradingView: %#v", tv.calls)
	}
}

func TestTradingViewTickerEndpoint(t *testing.T) {
	tv := &fakeTradingViewController{enabled: true}
	server := &Server{tv: tv}
	request := httptest.NewRequest(http.MethodPost, "/api/integrations/tradingview/ticker", strings.NewReader(`{"symbol":"msft"}`))
	request.RemoteAddr = "[::1]:41000"
	response := httptest.NewRecorder()
	server.tradingViewTicker(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if len(tv.calls) != 1 || tv.calls[0] != "MSFT" {
		t.Fatalf("TradingView calls = %#v", tv.calls)
	}
}

func TestTradingViewTickerEndpointRejectsRemoteClient(t *testing.T) {
	tv := &fakeTradingViewController{enabled: true}
	server := &Server{tv: tv}
	request := httptest.NewRequest(http.MethodPost, "/api/integrations/tradingview/ticker", strings.NewReader(`{"symbol":"MSFT"}`))
	request.RemoteAddr = "192.0.2.20:41000"
	response := httptest.NewRecorder()
	server.tradingViewTicker(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if len(tv.calls) != 0 {
		t.Fatalf("remote request reached TradingView: %#v", tv.calls)
	}
}
