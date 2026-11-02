package audit

import (
	"testing"
	"time"
)

// An object that is never Forgotten (its finalizer was removed by hand) must
// not stay in memory for good.
func TestTrackerDropsIdleEntries(t *testing.T) {
	clock := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	tr := NewTracker()
	tr.now = func() time.Time { return clock }

	if _, _, _, err := tr.Observe("gone", map[string]string{"size": "s"}); err != nil {
		t.Fatal(err)
	}
	tr.Notice("gone", "k", "v")

	// A live object is reconciled within a poll interval and keeps its entry.
	clock = clock.Add(idleTTL - time.Hour)
	if _, _, _, err := tr.Observe("live", map[string]string{"size": "s"}); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Hour)
	if _, old, _, err := tr.Observe("live", map[string]string{"size": "s"}); err != nil || old == "" {
		t.Fatalf("a live object lost its entry: old=%q err=%v", old, err)
	}

	tr.mu.Lock()
	_, seen := tr.seen["gone"]
	_, noticed := tr.notices["gone"]
	_, touched := tr.touched["gone"]
	tr.mu.Unlock()
	if seen || noticed || touched {
		t.Errorf("idle entry kept: seen=%v notices=%v touched=%v", seen, noticed, touched)
	}
	if !tr.Notice("gone", "k", "v") {
		t.Error("an evicted object's notice is new again")
	}
}

func TestTrackerSweepsAtMostOncePerInterval(t *testing.T) {
	clock := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	tr := NewTracker()
	tr.now = func() time.Time { return clock }
	tr.Notice("a", "k", "v")
	swept := tr.lastSweep

	clock = clock.Add(time.Minute)
	tr.Notice("b", "k", "v")
	if !tr.lastSweep.Equal(swept) {
		t.Errorf("swept again after a minute: %v -> %v", swept, tr.lastSweep)
	}
}
