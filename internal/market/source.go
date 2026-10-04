package market

import (
	"encoding/json"
	"sort"
	"time"
)

// Snapshot timestamps are nanoseconds; streaming timestamps and aggregate bar
// starts are milliseconds. Normalize without float64 precision loss.
func snapshotMillis(t int64) int64 {
	switch {
	case t >= 100000000000000000:
		return t / 1000000
	case t >= 100000000000000:
		return t / 1000
	default:
		return t
	}
}
func sourceDay(t int64, loc *time.Location) string {
	return time.UnixMilli(t).In(loc).Format("2006-01-02")
}
func sourceSession(q *Quote, day string) bool {
	if q.session != "" && day < q.session {
		return false
	}
	if day != q.session {
		q.session = day
		q.History = nil
		q.Last = 0
		q.Updated = 0
		q.Bid = 0
		q.Ask = 0
		q.BidAskUpdated = 0
		q.Volume = 0
		q.VolumeUpdated = 0
		q.PrevClose = 0
		q.ChangePct = 0
		q.liveRevision = map[int64]int64{}
		q.priceRevision = map[int64]int64{}
		q.volumeRevision = map[int64]int64{}
	}
	return true
}
func sourceChange(q *Quote) {
	if q.PrevClose > 0 && q.Last > 0 {
		q.ChangePct = (q.Last/q.PrevClose - 1) * 100
	} else {
		q.ChangePct = 0
	}
}
func sourcePoint(q *Quote, t int64) Point {
	for _, p := range q.History {
		if p.T == t {
			return p
		}
	}
	return Point{T: t}
}
func trimSource(q *Quote, maxPoints int) {
	if len(q.History) <= maxPoints {
		return
	}
	removed := q.History[:len(q.History)-maxPoints]
	for _, p := range removed {
		delete(q.liveRevision, p.T)
		delete(q.priceRevision, p.T)
		delete(q.volumeRevision, p.T)
	}
	q.History = q.History[len(q.History)-maxPoints:]
}
func (h *Hub) applySourceEvent(ev sourceEvent, feed string, loc *time.Location) {
	if ev.EventMS <= 0 {
		return
	}
	h.mu.Lock()
	defer func() { h.mu.Unlock(); h.signal() }()
	q := h.quotes[ev.Symbol]
	if q == nil {
		return
	} // removed/unknown symbol cannot recreate a subscription
	q.Source = "massive"
	q.Feed = feed
	if ev.Channel == "Q" {
		var v struct {
			Bid float64 `json:"bp"`
			Ask float64 `json:"ap"`
		}
		if json.Unmarshal(ev.Data, &v) != nil {
			return
		}
		// Quotes alone do not move the last-sale clock or erase last-session history.
		if ev.EventMS >= q.BidAskUpdated {
			q.Bid = v.Bid
			q.Ask = v.Ask
			q.BidAskUpdated = ev.EventMS
			q.Received = max(q.Received, ev.ReceivedMS)
		}
		return
	}
	if ev.Channel != "A" && ev.Channel != "AM" {
		return
	}
	var bar struct {
		Start int64   `json:"s"`
		End   int64   `json:"e"`
		C     float64 `json:"c"`
		V     float64 `json:"v"`
		AV    float64 `json:"av"`
	}
	if json.Unmarshal(ev.Data, &bar) != nil || bar.Start <= 0 || bar.C <= 0 {
		return
	}
	at := time.UnixMilli(bar.Start).In(loc)
	minute := at.Hour()*60 + at.Minute()
	if minute < 240 || minute >= 1200 {
		return
	}
	if !sourceSession(q, sourceDay(bar.Start, loc)) {
		return
	}
	t := bar.Start / 60000 * 60000
	p := sourcePoint(q, t)
	if ev.EventMS >= q.priceRevision[t] {
		p.P = bar.C
		q.priceRevision[t] = ev.EventMS
	}
	// AM is a complete/revised minute volume, NOT an incremental trade count.
	if ev.Channel == "AM" && ev.EventMS >= q.volumeRevision[t] {
		p.V = bar.V
		q.volumeRevision[t] = ev.EventMS
	}
	q.History = upsertPoint(q.History, p)
	q.liveRevision[t] = time.Now().UnixNano()
	trimSource(q, h.maxPoints)
	if ev.EventMS >= q.Updated {
		q.Last = bar.C
		q.Updated = ev.EventMS
	}
	if bar.AV > 0 && ev.EventMS >= q.VolumeUpdated {
		q.Volume = bar.AV
		q.VolumeUpdated = ev.EventMS
	}
	q.Received = max(q.Received, ev.ReceivedMS)
	sourceChange(q)
}
func (h *Hub) mergeSourceHistory(symbol string, points []Point, snapshot stockSnapshot, started int64, feed, message string, loc *time.Location) {
	h.mu.Lock()
	defer func() { h.mu.Unlock(); h.signal() }()
	q := h.quotes[symbol]
	if q == nil {
		return
	}
	q.Source = "massive"
	q.Feed = feed
	q.Error = message
	if len(points) > 0 {
		day := sourceDay(points[len(points)-1].T, loc)
		if sourceSession(q, day) {
			for _, p := range points {
				p.T = p.T / 60000 * 60000
				if q.liveRevision[p.T] > started {
					continue
				} // a slow backfill must not overwrite events received after it began
				old := sourcePoint(q, p.T)
				// REST does not expose the last trade time inside an active minute. Retain
				// the newer streamed close until this minute is complete.
				if q.priceRevision[p.T] > p.T && p.T/60000 == time.Now().UnixMilli()/60000 {
					p.P = old.P
				}
				q.History = upsertPoint(q.History, p)
			}
			sort.Slice(q.History, func(i, j int) bool { return q.History[i].T < q.History[j].T })
			trimSource(q, h.maxPoints)
			last := q.History[len(q.History)-1]
			if last.T > q.Updated {
				q.Last = last.P
				q.Updated = last.T
			}
			if q.VolumeUpdated == 0 {
				q.Volume = 0
				for _, p := range q.History {
					q.Volume += p.V
				}
			}
		}
	}
	s := snapshot.Ticker
	stamp := snapshotMillis(s.LastTrade.T)
	price := s.LastTrade.P
	if price <= 0 {
		price = s.Min.C
		stamp = snapshotMillis(s.Min.T)
	}
	if stamp > 0 && price > 0 && sourceSession(q, sourceDay(stamp, loc)) {
		if stamp >= q.Updated {
			q.Last = price
			q.Updated = stamp
		}
		// prevDay is Massive's previous daily close, not the last after-hours print.
		if s.PrevDay.C > 0 {
			q.PrevClose = s.PrevDay.C
		}
		if s.Day.V > 0 && stamp >= q.VolumeUpdated {
			q.Volume = s.Day.V
			q.VolumeUpdated = stamp
		}
	}
	qt := snapshotMillis(s.LastQuote.T)
	if qt > 0 && qt >= q.BidAskUpdated {
		q.Bid = s.LastQuote.P
		q.Ask = s.LastQuote.Ask
		q.BidAskUpdated = qt
	}
	// Receiving REST data is not a market update. Updated always remains a
	// provider timestamp; an old snapshot cannot turn a stale price fresh.
	sourceChange(q)
}
