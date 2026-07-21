package market

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scmhub/ibapi"
	"watchlist-tool/internal/config"
	"watchlist-tool/internal/model"
)

type Point struct {
	T int64   `json:"t"`
	P float64 `json:"p"`
	V float64 `json:"v,omitempty"`
}
type Quote struct {
	Symbol      string  `json:"symbol"`
	CompanyName string  `json:"company_name,omitempty"`
	Last        float64 `json:"last"`
	Bid         float64 `json:"bid"`
	Ask         float64 `json:"ask"`
	Volume      float64 `json:"volume"`
	ChangePct   float64 `json:"change_pct"`
	PrevClose   float64 `json:"prev_close"`
	Updated     int64   `json:"updated"`
	History     []Point `json:"history"`
}
type Status struct {
	State     string `json:"state"`
	Message   string `json:"message"`
	Connected bool   `json:"connected"`
	Updated   int64  `json:"updated"`
}
type Hub struct {
	mu        sync.RWMutex
	quotes    map[string]*Quote
	status    Status
	maxPoints int
	listeners map[chan struct{}]struct{}
}

// SetHistory replaces a symbol's intraday series with time-ordered minute data.
// A live point newer than the historical response is retained so a slow backfill
// cannot make the day map jump backwards.
func (h *Hub) SetHistory(symbol string, points []Point) {
	h.mu.Lock()
	q := h.quotes[symbol]
	if q == nil {
		q = &Quote{Symbol: symbol}
		h.quotes[symbol] = q
	}
	copyPoints := append([]Point(nil), points...)
	if n := len(q.History); n > 0 && (len(copyPoints) == 0 || q.History[n-1].T > copyPoints[len(copyPoints)-1].T) {
		copyPoints = append(copyPoints, q.History[n-1])
	}
	if len(copyPoints) > h.maxPoints {
		copyPoints = copyPoints[len(copyPoints)-h.maxPoints:]
	}
	q.History = copyPoints
	q.Volume = 0
	for _, point := range copyPoints {
		q.Volume += point.V
	}
	h.mu.Unlock()
	h.signal()
}
func (h *Hub) SetCompanyName(symbol, name string) {
	h.mu.Lock()
	if q := h.quotes[symbol]; q != nil {
		q.CompanyName = strings.TrimSpace(name)
	}
	h.mu.Unlock()
	h.signal()
}

func NewHub(max int) *Hub {
	return &Hub{quotes: map[string]*Quote{}, maxPoints: max, listeners: map[chan struct{}]struct{}{}}
}
func (h *Hub) Symbols(symbols []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range symbols {
		if h.quotes[s] == nil {
			h.quotes[s] = &Quote{Symbol: s}
		}
	}
	go h.signal()
}
func (h *Hub) Update(symbol string, fn func(*Quote)) {
	h.mu.Lock()
	q := h.quotes[symbol]
	if q == nil {
		q = &Quote{Symbol: symbol}
		h.quotes[symbol] = q
	}
	old := q.Last
	fn(q)
	q.Updated = time.Now().UnixMilli()
	if q.PrevClose > 0 && q.Last > 0 {
		q.ChangePct = (q.Last/q.PrevClose - 1) * 100
	}
	if q.Last > 0 && (old != q.Last || len(q.History) == 0) {
		now := time.Now().UnixMilli()
		if n := len(q.History); n > 0 && q.History[n-1].T/60000 == now/60000 {
			q.History[n-1].P = q.Last
			q.History[n-1].T = now
		} else {
			q.History = append(q.History, Point{T: now, P: q.Last})
		}
		if len(q.History) > h.maxPoints {
			q.History = q.History[len(q.History)-h.maxPoints:]
		}
	}
	h.mu.Unlock()
	h.signal()
}
func (h *Hub) SetStatus(s Status) {
	s.Updated = time.Now().UnixMilli()
	h.mu.Lock()
	h.status = s
	h.mu.Unlock()
	h.signal()
}
func (h *Hub) Snapshot() (map[string]Quote, Status) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]Quote, len(h.quotes))
	for k, v := range h.quotes {
		x := *v
		x.History = append([]Point(nil), v.History...)
		out[k] = x
	}
	return out, h.status
}
func (h *Hub) Subscribe() chan struct{} {
	c := make(chan struct{}, 1)
	h.mu.Lock()
	h.listeners[c] = struct{}{}
	h.mu.Unlock()
	return c
}
func (h *Hub) Unsubscribe(c chan struct{}) {
	h.mu.Lock()
	delete(h.listeners, c)
	close(c)
	h.mu.Unlock()
}
func (h *Hub) signal() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.listeners {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

type IBKR struct {
	cfg        config.Config
	hub        *Hub
	mu         sync.RWMutex
	client     *ibapi.EClient
	req        map[int64]string
	historyReq map[int64]string
	history    map[int64][]Point
	detailsReq map[int64]string
	next       int64
	wanted     map[string]bool
}
type wrapper struct {
	ibapi.Wrapper
	feed  *IBKR
	ready chan struct{}
	once  sync.Once
}

func NewIBKR(c config.Config, h *Hub) *IBKR {
	return &IBKR{cfg: c, hub: h, req: map[int64]string{}, historyReq: map[int64]string{}, history: map[int64][]Point{}, detailsReq: map[int64]string{}, wanted: map[string]bool{}, next: 2000}
}
func (f *IBKR) SetSymbols(ss []string) {
	desired := make(map[string]bool, len(ss))
	for _, s := range ss {
		if s = model.Symbol(s); s != "" {
			desired[s] = true
		}
	}
	f.mu.Lock()
	for s := range f.wanted {
		if desired[s] {
			continue
		}
		delete(f.wanted, s)
		for id, subscribed := range f.req {
			if subscribed == s {
				if f.client != nil && f.client.IsConnected() {
					f.client.CancelMktData(id)
				}
				delete(f.req, id)
			}
		}
		for id, subscribed := range f.historyReq {
			if subscribed == s {
				if f.client != nil && f.client.IsConnected() {
					f.client.CancelHistoricalData(id)
				}
				delete(f.historyReq, id)
				delete(f.history, id)
			}
		}
	}
	for s := range desired {
		if !f.wanted[s] {
			f.wanted[s] = true
			if f.client != nil && f.client.IsConnected() {
				f.subscribeLocked(s)
			}
		}
	}
	f.mu.Unlock()
	f.hub.Symbols(ss)
}
func (f *IBKR) Run(ctx context.Context) {
	delay, _ := time.ParseDuration(f.cfg.IBKR.ReconnectInterval)
	ibapi.SetLogLevel(2)
	for ctx.Err() == nil {
		f.hub.SetStatus(Status{State: "connecting", Message: fmt.Sprintf("%s:%d · client %d", f.cfg.IBKR.Host, f.cfg.IBKR.Port, f.cfg.IBKR.ClientID)})
		w := &wrapper{feed: f, ready: make(chan struct{})}
		c := ibapi.NewEClient(w)
		if err := c.Connect(f.cfg.IBKR.Host, f.cfg.IBKR.Port, f.cfg.IBKR.ClientID); err != nil {
			f.hub.SetStatus(Status{State: "waiting", Message: err.Error()})
			if !wait(ctx, delay) {
				return
			}
			continue
		}
		select {
		case <-w.ready:
		case <-time.After(4 * time.Second):
			_ = c.Disconnect()
			if !wait(ctx, delay) {
				return
			}
			continue
		case <-ctx.Done():
			_ = c.Disconnect()
			return
		}
		f.mu.Lock()
		f.client = c
		c.ReqMarketDataType(f.cfg.IBKR.MarketDataType)
		for symbol := range f.wanted {
			f.subscribeLocked(symbol)
		}
		f.mu.Unlock()
		f.hub.SetStatus(Status{State: "live", Connected: true, Message: "IBKR live"})
		for c.IsConnected() && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
		_ = c.Disconnect()
		f.mu.Lock()
		f.client = nil
		f.req = map[int64]string{}
		f.historyReq = map[int64]string{}
		f.history = map[int64][]Point{}
		f.detailsReq = map[int64]string{}
		f.mu.Unlock()
		if !wait(ctx, delay) {
			return
		}
	}
}
func (f *IBKR) subscribeLocked(s string) {
	f.next++
	id := f.next
	f.req[id] = s
	contract := &ibapi.Contract{Symbol: s, SecType: "STK", Exchange: f.cfg.IBKR.Exchange, Currency: f.cfg.IBKR.Currency}
	f.client.ReqMktData(id, contract, "233", false, false, nil)
	f.next++
	historyID := f.next
	f.historyReq[historyID] = s
	f.history[historyID] = nil
	// A full US extended-hours session at one-minute resolution. Epoch timestamps
	// (formatDate=2) keep timezone conversion in the browser unambiguous.
	// keepUpToDate makes IBKR refresh the current bar every few seconds. This is
	// both lighter and more reliable than reconstructing volume from individual
	// RT_VOLUME messages, which can have gaps around the initial backfill.
	f.client.ReqHistoricalData(historyID, contract, "", "1 D", "1 min", "TRADES", false, 2, true, nil)
	f.next++
	detailsID := f.next
	f.detailsReq[detailsID] = s
	f.client.ReqContractDetails(detailsID, contract)
}
func (f *IBKR) symbol(id int64) string  { f.mu.RLock(); defer f.mu.RUnlock(); return f.req[id] }
func (w *wrapper) NextValidID(id int64) { w.once.Do(func() { close(w.ready) }) }
func (w *wrapper) ConnectionClosed() {
	w.feed.hub.SetStatus(Status{State: "reconnecting", Message: "IBKR connection closed"})
}
func (w *wrapper) Error(id ibapi.TickerID, at, code int64, msg, advanced string) {
	if code >= 2100 && code <= 2199 {
		return
	}
	log.Printf("IBKR error req=%d code=%d message=%q", id, code, msg)
	w.feed.hub.SetStatus(Status{State: "degraded", Connected: true, Message: fmt.Sprintf("IBKR %d: %s", code, msg)})
}
func (w *wrapper) TickPrice(id ibapi.TickerID, t ibapi.TickType, p float64, a ibapi.TickAttrib) {
	s := w.feed.symbol(id)
	if s == "" || p <= 0 {
		return
	}
	w.feed.hub.Update(s, func(q *Quote) {
		switch t {
		case ibapi.LAST, ibapi.DELAYED_LAST:
			q.Last = p
		case ibapi.BID, ibapi.DELAYED_BID:
			q.Bid = p
		case ibapi.ASK, ibapi.DELAYED_ASK:
			q.Ask = p
		case ibapi.CLOSE, ibapi.DELAYED_CLOSE:
			q.PrevClose = p
		}
	})
}
func (w *wrapper) TickSize(id ibapi.TickerID, t ibapi.TickType, size ibapi.Decimal) {
	// Volume is intentionally sourced from the continuously updated 04:00 ET
	// minute bars. IBKR's generic VOLUME tick does not define that boundary.
}
func (w *wrapper) TickString(id ibapi.TickerID, t ibapi.TickType, value string) {
	s := w.feed.symbol(id)
	if s == "" || t != ibapi.RT_VOLUME {
		return
	}
	p := strings.Split(value, ";")
	if len(p) < 4 {
		return
	}
	last, _ := strconv.ParseFloat(p[0], 64)
	if math.IsNaN(last) {
		return
	}
	w.feed.hub.Update(s, func(q *Quote) {
		if last > 0 {
			q.Last = last
		}
	})
}
func (w *wrapper) HistoricalData(id int64, bar *ibapi.Bar) {
	w.feed.mu.Lock()
	if _, ok := w.feed.historyReq[id]; ok {
		seconds, err := strconv.ParseInt(bar.Date, 10, 64)
		if err == nil && bar.Close > 0 {
			w.feed.history[id] = append(w.feed.history[id], Point{T: seconds * 1000, P: bar.Close, V: bar.Volume.Float()})
		}
	}
	w.feed.mu.Unlock()
}
func (w *wrapper) HistoricalDataEnd(id int64, _, _ string) {
	w.feed.mu.Lock()
	symbol := w.feed.historyReq[id]
	points := append([]Point(nil), w.feed.history[id]...)
	w.feed.mu.Unlock()
	w.publishHistory(symbol, points)
}

// HistoricalDataUpdate is emitted for a keepUpToDate request as IBKR revises
// the active minute bar. Replacing that bar keeps cumulative 04:00 ET volume
// current without issuing a fresh historical request for every ticker.
func (w *wrapper) HistoricalDataUpdate(id int64, bar *ibapi.Bar) {
	seconds, err := strconv.ParseInt(bar.Date, 10, 64)
	if err != nil || bar.Close <= 0 {
		return
	}
	point := Point{T: seconds * 1000, P: bar.Close, V: bar.Volume.Float()}
	w.feed.mu.Lock()
	symbol := w.feed.historyReq[id]
	if symbol != "" {
		w.feed.history[id] = upsertPoint(w.feed.history[id], point)
	}
	points := append([]Point(nil), w.feed.history[id]...)
	w.feed.mu.Unlock()
	w.publishHistory(symbol, points)
}

func (w *wrapper) publishHistory(symbol string, points []Point) {
	if symbol == "" {
		return
	}
	loc, err := time.LoadLocation(w.feed.cfg.App.Timezone)
	if err == nil {
		points = extendedSession(points, loc)
	}
	w.feed.hub.SetHistory(symbol, points)
}

func upsertPoint(points []Point, point Point) []Point {
	for i := len(points) - 1; i >= 0; i-- {
		if points[i].T == point.T {
			points[i] = point
			return points
		}
		if points[i].T < point.T {
			points = append(points, Point{})
			copy(points[i+2:], points[i+1:])
			points[i+1] = point
			return points
		}
	}
	return append([]Point{point}, points...)
}

func extendedSession(points []Point, loc *time.Location) []Point {
	if len(points) == 0 {
		return points
	}
	latest := time.UnixMilli(points[len(points)-1].T).In(loc)
	year, month, day := latest.Date()
	out := make([]Point, 0, len(points))
	for _, point := range points {
		at := time.UnixMilli(point.T).In(loc)
		y, m, d := at.Date()
		minute := at.Hour()*60 + at.Minute()
		if y == year && m == month && d == day && minute >= 4*60 && minute <= 20*60 {
			out = append(out, point)
		}
	}
	return out
}
func (w *wrapper) ContractDetails(id int64, details *ibapi.ContractDetails) {
	w.feed.mu.RLock()
	symbol := w.feed.detailsReq[id]
	w.feed.mu.RUnlock()
	if symbol != "" && details.LongName != "" {
		w.feed.hub.SetCompanyName(symbol, details.LongName)
	}
}
func (w *wrapper) ContractDetailsEnd(id int64) {
	w.feed.mu.Lock()
	delete(w.feed.detailsReq, id)
	w.feed.mu.Unlock()
}
func wait(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

type Demo struct {
	hub     *Hub
	mu      sync.Mutex
	symbols []string
}

func NewDemo(h *Hub) *Demo { return &Demo{hub: h} }
func (d *Demo) SetSymbols(s []string) {
	d.mu.Lock()
	d.symbols = append([]string(nil), s...)
	d.mu.Unlock()
	d.hub.Symbols(s)
	now := time.Now()
	for i, symbol := range s {
		base := 20 + float64((i*37)%180)
		points := make([]Point, 0, 720)
		start := time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, now.Location())
		for minute := 0; minute <= 720; minute++ {
			price := base + math.Sin(float64(minute+i)*.035)*base*.018 + float64(minute)*base*.000015
			points = append(points, Point{T: start.Add(time.Duration(minute) * time.Minute).UnixMilli(), P: price})
		}
		d.hub.SetHistory(symbol, points)
		d.hub.SetCompanyName(symbol, symbol+" DEMO COMPANY")
	}
}
func (d *Demo) Run(ctx context.Context) {
	d.hub.SetStatus(Status{State: "demo", Connected: true, Message: "synthetic market feed"})
	tick := time.NewTicker(650 * time.Millisecond)
	defer tick.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			n++
			d.mu.Lock()
			ss := append([]string(nil), d.symbols...)
			d.mu.Unlock()
			for i, s := range ss {
				base := 20 + float64((i*37)%180)
				p := base + math.Sin(float64(n+i)*.19)*base*.025 + float64((n+i)%7)*.01
				d.hub.Update(s, func(q *Quote) {
					if q.PrevClose == 0 {
						q.PrevClose = base
					}
					q.Last = p
					q.Bid = p - .01
					q.Ask = p + .01
					q.Volume += float64(500 + (n*31+i*97)%8000)
				})
			}
		}
	}
}
