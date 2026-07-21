package model

import "testing"

func TestRepeatedScannerAlertPromotesWithoutDuplicate(t *testing.T) {
	s, err := Open(t.TempDir() + "/watchlists.json")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Add("nvda", "watch", "first alert")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Add("aapl", "watch", ""); err != nil {
		t.Fatal(err)
	}
	again, err := s.Add("NVDA", "focus", "second alert")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Fatalf("duplicate received new ID %q != %q", again.ID, first.ID)
	}
	got := s.Get()
	if len(got.Lists[0].Items) != 1 || got.Lists[0].Items[0].Symbol != "NVDA" {
		t.Fatalf("focus=%+v", got.Lists[0].Items)
	}
	if got.Lists[0].Items[0].Note != "second alert" {
		t.Fatalf("note=%q", got.Lists[0].Items[0].Note)
	}
	if len(got.Lists[1].Items) != 1 || got.Lists[1].Items[0].Symbol != "AAPL" {
		t.Fatalf("watch=%+v", got.Lists[1].Items)
	}
}

func TestInvalidSymbolRejected(t *testing.T) {
	s, _ := Open(t.TempDir() + "/watchlists.json")
	if _, err := s.Add("bad symbol", "focus", ""); err == nil {
		t.Fatal("expected invalid symbol error")
	}
}
