package market

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"watchlist-tool/internal/config"
	"watchlist-tool/internal/marketgateway"
	"watchlist-tool/internal/model"
)

// Gateway holds no Massive credentials and never constructs a Massive URL.
// It consumes the gateway's versioned REST and SSE contract exclusively.
type Gateway struct {
	cfg     config.Config
	hub     *Hub
	client  *marketgateway.Client
	mu      sync.Mutex
	symbols []string
	changed chan struct{}
	loc     *time.Location
}

func NewGateway(c config.Config, h *Hub) *Gateway {
	loc, _ := time.LoadLocation(c.App.Timezone)
	client, _ := marketgateway.New(c.Gateway.APIURL, c.Gateway.Token)
	return &Gateway{cfg: c, hub: h, loc: loc, changed: make(chan struct{}, 1), client: client}
}
func (f *Gateway) SetSymbols(ss []string) {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range ss {
		if s = model.Symbol(s); s != "" && !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	sort.Strings(out)
	f.mu.Lock()
	same := strings.Join(out, ",") == strings.Join(f.symbols, ",")
	f.symbols = out
	f.mu.Unlock()
	f.hub.mu.Lock()
	for s := range f.hub.quotes {
		if !seen[s] {
			delete(f.hub.quotes, s)
		}
	}
	f.hub.mu.Unlock()
	f.hub.Symbols(out)
	if !same {
		select {
		case f.changed <- struct{}{}:
		default:
		}
	}
}
func (f *Gateway) wanted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.symbols...)
}
func (f *Gateway) Run(ctx context.Context) {
	delay, _ := time.ParseDuration(f.cfg.Gateway.ReconnectInterval)
	defer f.client.Close()
	for ctx.Err() == nil {
		// Drain changes BEFORE reading the desired set to avoid canceling the new stream.
		select {
		case <-f.changed:
		default:
		}
		ss := f.wanted()
		if len(ss) == 0 || len(ss) > 200 {
			msg := "Add a stock to this independent Massive watchlist"
			if len(ss) > 200 {
				msg = "Gateway limit: reduce this watchlist to 200 distinct symbols"
			}
			f.hub.SetStatus(Status{State: "waiting", Message: msg})
			select {
			case <-ctx.Done():
				return
			case <-f.changed:
				continue
			}
		}
		f.hub.SetStatus(Status{State: "connecting", Message: "Gateway market-data gateway"})
		cycle, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- f.cycle(cycle, ss) }()
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return
		case <-f.changed:
			cancel()
			<-done
			continue
		case err := <-done:
			cancel()
			message := "Gateway stream ended; resynchronizing"
			if err != nil {
				message = err.Error()
			}
			f.hub.SetStatus(Status{State: "offline", Message: message})
		}
		select {
		case <-ctx.Done():
			return
		case <-f.changed:
		case <-time.After(delay):
		}
	}
}
func (f *Gateway) request(ctx context.Context, path string) (*http.Response, error) {
	return f.client.Request(ctx, path)
}
func (f *Gateway) get(ctx context.Context, path string, out any) error {
	return f.client.Get(ctx, path, out)
}

type sourceEvent struct {
	Channel, Symbol string
	EventMS         int64 `json:"event_ms"`
	ReceivedMS      int64 `json:"received_ms"`
	Data            json.RawMessage
}

func (f *Gateway) cycle(ctx context.Context, ss []string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	q := url.Values{"symbols": {strings.Join(ss, ",")}, "channels": {f.cfg.Gateway.Channels}}
	res, e := f.request(ctx, "/stream?"+q.Encode())
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		return errors.New("Gateway stream endpoint is unavailable; update the gateway")
	}
	var last atomic.Int64
	last.Store(time.Now().UnixMilli())
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if time.Now().UnixMilli()-last.Load() > 20000 {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-watchdogDone }()
	var refreshDone chan struct{}
	defer func() {
		cancel()
		if refreshDone != nil {
			<-refreshDone
		}
	}()
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	event := ""
	body := ""
	feed := ""
	hello := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(line[6:])
			continue
		}
		if strings.HasPrefix(line, "data:") {
			body += strings.TrimSpace(line[5:])
			continue
		}
		if line != "" {
			continue
		}
		last.Store(time.Now().UnixMilli())
		switch event {
		case "hello":
			var h struct {
				Source, Feed string
				APIVersion   int `json:"api_version"`
			}
			if e = json.Unmarshal([]byte(body), &h); e != nil || h.Source != "massive" || h.APIVersion != 1 {
				return errors.New("unsupported Gateway market-data contract")
			}
			if hello {
				return errors.New("unexpected second stream handshake")
			}
			hello = true
			feed = h.Feed
			f.hub.SetStatus(Status{State: "connected", Connected: true, Message: "Massive via gateway · " + feed + " · source timestamps; stale rows are marked"})
			refreshDone = make(chan struct{})
			go func() { defer close(refreshDone); f.refreshLoop(ctx, ss, feed) }()
		case "market":
			if !hello {
				return errors.New("missing gateway handshake")
			}
			var ev sourceEvent
			if e = json.Unmarshal([]byte(body), &ev); e != nil {
				return errors.New("invalid market event; resync required")
			}
			f.hub.applySourceEvent(ev, feed, f.loc)
		case "gap":
			return errors.New("Gateway reported a stream gap; rebuilding from REST")
		case "heartbeat":
			if !hello {
				return errors.New("missing gateway handshake")
			}
		}
		event = ""
		body = ""
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("Gateway stream disconnected; REST resync required")
}
func (f *Gateway) refreshLoop(ctx context.Context, ss []string, feed string) {
	period, _ := time.ParseDuration(f.cfg.Gateway.RefreshInterval)
	tick := time.NewTicker(period)
	defer tick.Stop()
	for {
		jobs := make(chan string)
		var wg sync.WaitGroup
		for i := 0; i < min(4, len(ss)); i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for s := range jobs {
					f.hydrate(ctx, s, feed)
				}
			}()
		}
		for _, s := range ss {
			select {
			case jobs <- s:
			case <-ctx.Done():
				close(jobs)
				wg.Wait()
				return
			}
		}
		close(jobs)
		wg.Wait()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

type stockSnapshot struct {
	Ticker struct {
		Updated int64 `json:"updated"`
		Day     struct {
			V float64 `json:"v"`
		} `json:"day"`
		PrevDay struct {
			C float64 `json:"c"`
		} `json:"prevDay"`
		LastTrade struct {
			P float64 `json:"p"`
			T int64   `json:"t"`
		} `json:"lastTrade"`
		LastQuote struct {
			P   float64 `json:"p"`
			Ask float64 `json:"P"`
			T   int64   `json:"t"`
		} `json:"lastQuote"`
		Min struct {
			C float64 `json:"c"`
			T int64   `json:"t"`
		} `json:"min"`
	} `json:"ticker"`
}

func (f *Gateway) hydrate(ctx context.Context, symbol, feed string) {
	started := time.Now().UnixNano()
	s := url.PathEscape(symbol)
	var snap stockSnapshot
	snapErr := f.get(ctx, "/rest/v2/snapshot/locale/us/markets/stocks/tickers/"+s, &snap)
	now := time.Now().In(f.loc)
	path := "/rest/v2/aggs/ticker/" + s + "/range/1/minute/" + now.AddDate(0, 0, -10).Format("2006-01-02") + "/" + now.Format("2006-01-02") + "?adjusted=false&sort=asc&limit=50000"
	points := []Point{}
	var barsErr error
	for page := 0; path != ""; page++ {
		if page >= 5 {
			barsErr = errors.New("history pagination limit exceeded")
			break
		}
		var bars struct {
			Results []struct {
				T int64   `json:"t"`
				C float64 `json:"c"`
				V float64 `json:"v"`
			} `json:"results"`
			Next string `json:"next_url"`
		}
		if barsErr = f.get(ctx, path, &bars); barsErr != nil {
			break
		}
		for _, b := range bars.Results {
			if b.C > 0 {
				points = append(points, Point{T: b.T, P: b.C, V: b.V})
			}
		}
		if len(points) > 20000 {
			barsErr = errors.New("history response too large")
			break
		}
		path = bars.Next
		if path != "" && (!strings.Contains(path, "/rest/v2/aggs/ticker/"+s+"/") || strings.Contains(path, "..")) {
			barsErr = errors.New("unsafe pagination from gateway")
			break
		}
	}
	if ctx.Err() != nil {
		return
	}
	if barsErr == nil {
		sort.Slice(points, func(i, j int) bool { return points[i].T < points[j].T })
		points = extendedSession(points, f.loc)
	} else {
		points = nil
	}
	msg := ""
	if barsErr != nil {
		msg = "History: " + barsErr.Error()
	}
	if snapErr != nil {
		if msg != "" {
			msg += "; "
		}
		msg += "Snapshot: " + snapErr.Error()
	}
	f.hub.mergeSourceHistory(symbol, points, snap, started, feed, msg, f.loc)
	// Metadata is cached centrally for one hour; absent metadata does not block prices.
	var meta struct {
		Results struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if f.get(ctx, "/rest/v3/reference/tickers/"+s, &meta) == nil && ctx.Err() == nil {
		f.hub.SetCompanyName(symbol, meta.Results.Name)
	}
}
