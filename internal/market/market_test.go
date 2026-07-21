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
