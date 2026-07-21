package market

import (
	"testing"
	"time"
)

func TestExtendedSessionAndVolume(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	at := func(day, hour, minute int) int64 {
		return time.Date(2026, time.July, day, hour, minute, 0, 0, loc).UnixMilli()
	}
	points := []Point{
		{T: at(20, 19, 59), P: 9, V: 99},
		{T: at(21, 3, 59), P: 10, V: 100},
		{T: at(21, 4, 0), P: 11, V: 125},
		{T: at(21, 9, 30), P: 12, V: 250},
		{T: at(21, 20, 0), P: 13, V: 50},
		{T: at(21, 20, 1), P: 14, V: 500},
	}
	got := extendedSession(points, loc)
	if len(got) != 3 {
		t.Fatalf("got %d session points, want 3", len(got))
	}
	hub := NewHub(1000)
	hub.SetHistory("TEST", got)
	quotes, _ := hub.Snapshot()
	if quotes["TEST"].Volume != 425 {
		t.Fatalf("got volume %.0f, want 425", quotes["TEST"].Volume)
	}
}

func TestLiveHistoryCoalescesWithinMinute(t *testing.T) {
	hub := NewHub(1000)
	hub.Update("TEST", func(q *Quote) { q.Last = 10 })
	hub.Update("TEST", func(q *Quote) { q.Last = 11 })
	quotes, _ := hub.Snapshot()
	if got := len(quotes["TEST"].History); got != 1 {
		t.Fatalf("got %d live points in one minute, want 1", got)
	}
	if got := quotes["TEST"].History[0].P; got != 11 {
		t.Fatalf("got latest minute price %.2f, want 11", got)
	}
}

func TestUpsertPointReplacesLiveBarVolume(t *testing.T) {
	points := []Point{{T: 1000, P: 10, V: 100}, {T: 2000, P: 11, V: 25}}
	points = upsertPoint(points, Point{T: 2000, P: 12, V: 75})
	if len(points) != 2 || points[1].P != 12 || points[1].V != 75 {
		t.Fatalf("active bar was not replaced: %#v", points)
	}
	points = upsertPoint(points, Point{T: 1500, P: 10.5, V: 20})
	if len(points) != 3 || points[1].T != 1500 {
		t.Fatalf("new bar was not inserted in timestamp order: %#v", points)
	}
}

func TestIgnoreExpectedHistoricalCancellation(t *testing.T) {
	if !ignoreIBKRError(162, "Historical Market Data Service error message:API historical data query cancelled: 2014") {
		t.Fatal("expected client-side historical cancellation to be ignored")
	}
	if ignoreIBKRError(162, "Historical data request pacing violation") {
		t.Fatal("real historical data errors must remain visible")
	}
}
