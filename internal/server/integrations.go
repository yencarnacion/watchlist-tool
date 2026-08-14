package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"watchlist-tool/internal/model"
)

type selectionResult struct {
	Symbol             string `json:"symbol"`
	TapeOK             bool   `json:"tape_ok"`
	TapeError          string `json:"tape_error,omitempty"`
	TradingViewEnabled bool   `json:"tradingview_enabled"`
	TradingViewOK      bool   `json:"tradingview_ok"`
	TradingViewError   string `json:"tradingview_error,omitempty"`
}

func (s *Server) selectIntegrations(ctx context.Context, symbol string, allowTradingView bool) selectionResult {
	result := selectionResult{
		Symbol:             symbol,
		TradingViewEnabled: s.tv != nil && s.tv.Enabled(),
	}

	// Both destinations are independent and can be unavailable separately. Run
	// them concurrently so a stopped TradingView instance does not delay Tape
	// Reading Tool, and vice versa.
	tapeResult := make(chan error, 1)
	go func() { tapeResult <- s.sendToTape(ctx, symbol) }()

	tradingViewResult := make(chan error, 1)
	switch {
	case !result.TradingViewEnabled:
		tradingViewResult <- nil
	case !allowTradingView:
		tradingViewResult <- errors.New("TradingView control is limited to local requests")
	default:
		go func() { tradingViewResult <- s.tv.SetSymbol(ctx, symbol) }()
	}

	tapeErr := <-tapeResult
	tradingViewErr := <-tradingViewResult
	result.TapeOK = tapeErr == nil
	result.TapeError = errText(tapeErr)
	result.TradingViewOK = result.TradingViewEnabled && tradingViewErr == nil
	result.TradingViewError = errText(tradingViewErr)
	return result
}

func (s *Server) sendToTape(ctx context.Context, symbol string) error {
	payload, err := json.Marshal(map[string]string{"symbol": symbol})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Integrations.TapeURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("prepare Tape Reading Tool request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = response.Status
	}
	return fmt.Errorf("Tape Reading Tool rejected the ticker: %s", message)
}

func (s *Server) registerTradingViewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/integrations/tradingview/status", s.tradingViewStatus)
	mux.HandleFunc("/api/integrations/tradingview/ticker", s.tradingViewTicker)
}

func (s *Server) tradingViewStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if s.tv == nil {
		write(w, http.StatusOK, map[string]bool{"enabled": false, "connected": false})
		return
	}
	write(w, http.StatusOK, s.tv.Status(r.Context()))
}

func (s *Server) tradingViewTicker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !loopbackRequest(r) {
		http.Error(w, "TradingView control is limited to local requests", http.StatusForbidden)
		return
	}
	if s.tv == nil || !s.tv.Enabled() {
		http.Error(w, "TradingView Desktop integration is disabled", http.StatusServiceUnavailable)
		return
	}

	defer r.Body.Close()
	var input struct {
		Symbol string `json:"symbol"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		bad(w, "invalid JSON")
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		bad(w, "invalid JSON")
		return
	}
	symbol := model.Symbol(input.Symbol)
	if symbol == "" {
		bad(w, "invalid symbol")
		return
	}
	if err := s.tv.SetSymbol(r.Context(), symbol); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		http.Error(w, err.Error(), status)
		return
	}
	write(w, http.StatusOK, map[string]string{"symbol": symbol})
}

func loopbackRequest(r *http.Request) bool {
	host := strings.TrimSpace(r.RemoteAddr)
	if value, _, err := net.SplitHostPort(host); err == nil {
		host = value
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
