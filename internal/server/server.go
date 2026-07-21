package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"watchlist-tool/internal/config"
	"watchlist-tool/internal/market"
	"watchlist-tool/internal/model"
)

//go:embed web/*
var assets embed.FS

type SymbolSetter interface{ SetSymbols([]string) }
type Server struct {
	cfg    config.Config
	store  *model.Store
	hub    *market.Hub
	feed   SymbolSetter
	client *http.Client
}

func New(c config.Config, s *model.Store, h *market.Hub, f SymbolSetter) *Server {
	return &Server{cfg: c, store: s, hub: h, feed: f, client: &http.Client{Timeout: 1200 * time.Millisecond}}
}
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", s.state)
	mux.HandleFunc("/api/tickers", s.tickers)
	mux.HandleFunc("/api/tickers/", s.ticker)
	mux.HandleFunc("/api/layout", s.layout)
	mux.HandleFunc("/api/select", s.selectTicker)
	mux.HandleFunc("/api/chart", s.chart)
	mux.HandleFunc("/api/events", s.events)
	sub, _ := fs.Sub(assets, "web")
	mux.Handle("/", headers(http.FileServer(http.FS(sub))))
	srv := &http.Server{Addr: s.cfg.App.Addr, Handler: mux, ReadHeaderTimeout: 4 * time.Second, IdleTimeout: 60 * time.Second}
	ch := make(chan error, 1)
	go func() { ch <- srv.ListenAndServe() }()
	select {
	case err := <-ch:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		c, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
		return nil
	}
}
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	q, st := s.hub.Snapshot()
	write(w, 200, map[string]any{"layout": s.store.Get(), "quotes": q, "status": st, "config": map[string]any{"polygon_url": s.cfg.Integrations.PolygonURL, "timezone": s.cfg.App.Timezone}})
}
func (s *Server) tickers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var in struct{ Symbol, Ticker, ListID, Note string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		bad(w, "invalid JSON")
		return
	}
	if in.Symbol == "" {
		in.Symbol = in.Ticker
	}
	it, err := s.store.Add(in.Symbol, in.ListID, in.Note)
	if err != nil {
		bad(w, err.Error())
		return
	}
	s.sync()
	write(w, 201, it)
}
func (s *Server) ticker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		method(w)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/tickers/")
	if err := s.store.Delete(id); err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	s.sync()
	w.WriteHeader(204)
}
func (s *Server) layout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		method(w)
		return
	}
	var v model.Layout
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&v); err != nil {
		bad(w, "invalid JSON")
		return
	}
	if err := s.store.Replace(v); err != nil {
		bad(w, err.Error())
		return
	}
	s.sync()
	write(w, 200, v)
}
func (s *Server) selectTicker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var in struct{ Symbol string }
	if json.NewDecoder(r.Body).Decode(&in) != nil || model.Symbol(in.Symbol) == "" {
		bad(w, "invalid symbol")
		return
	}
	symbol := model.Symbol(in.Symbol)
	body := strings.NewReader(`{"symbol":"` + symbol + `"}`)
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, s.cfg.Integrations.TapeURL, body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	tapeOK := err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300
	if resp != nil {
		resp.Body.Close()
	}
	write(w, 200, map[string]any{"symbol": symbol, "tape_ok": tapeOK, "tape_error": errText(err)})
}
func (s *Server) chart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var in struct{ Symbol string }
	if json.NewDecoder(r.Body).Decode(&in) != nil || model.Symbol(in.Symbol) == "" {
		bad(w, "invalid symbol")
		return
	}
	symbol := model.Symbol(in.Symbol)
	loc, _ := time.LoadLocation(s.cfg.App.Timezone)
	chartURL := strings.TrimRight(s.cfg.Integrations.PolygonURL, "/") + "/api/open-chart/" + url.PathEscape(symbol) + "/" + time.Now().In(loc).Format("2006-01-02")
	write(w, 200, map[string]any{"symbol": symbol, "chart_url": chartURL})
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	c := s.hub.Subscribe()
	defer s.hub.Unsubscribe(c)
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	updates := time.NewTicker(200 * time.Millisecond)
	defer updates.Stop()
	dirty := false
	for {
		select {
		case <-r.Context().Done():
			return
		case <-c:
			dirty = true
		case <-updates.C:
			if !dirty {
				continue
			}
			dirty = false
			q, st := s.hub.Snapshot()
			b, _ := json.Marshal(map[string]any{"layout": s.store.Get(), "quotes": q, "status": st})
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}
func (s *Server) sync() {
	l := s.store.Get()
	var ss []string
	for _, list := range l.Lists {
		for _, it := range list.Items {
			ss = append(ss, it.Symbol)
		}
	}
	s.feed.SetSymbols(ss)
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func bad(w http.ResponseWriter, m string) { http.Error(w, m, 400) }
func method(w http.ResponseWriter)        { http.Error(w, "method not allowed", 405) }
func errText(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}
func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
func (s *Server) InitialSync() { s.sync() }
