package cloud

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// counting is a source that issues "t1", "t2", ... each valid for ttl from the
// fake clock, and counts its calls.
type counting struct {
	mu    sync.Mutex
	n     int
	ttl   time.Duration
	clock *time.Time
	err   error
	empty bool
}

func (c *counting) Token(context.Context) (string, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	if c.err != nil {
		return "", time.Time{}, c.err
	}
	if c.empty {
		return "", time.Time{}, nil
	}
	var exp time.Time
	if c.ttl != 0 {
		exp = c.clock.Add(c.ttl)
	}
	return fmt.Sprintf("t%d", c.n), exp, nil
}

func (c *counting) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func cached(src TokenSource, margin time.Duration, clock *time.Time) *CachedSource {
	c := NewCachedSource(src, margin)
	c.now = func() time.Time { return *clock }
	return c
}

func TestCachedSourceRefreshesBeforeExpiry(t *testing.T) {
	clock := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	src := &counting{ttl: 15 * time.Minute, clock: &clock}
	c := cached(src, 2*time.Minute, &clock)

	steps := []struct {
		advance time.Duration
		want    string
	}{
		{0, "t1"},
		{5 * time.Minute, "t1"},  // 10 minutes left
		{7 * time.Minute, "t1"},  // 3 minutes left, outside the margin
		{time.Minute, "t2"},      // 2 minutes left: inside the margin, so refreshed
		{12 * time.Minute, "t2"}, // 2 minutes + 12 > the new token's life? new token has 15m from the refresh: 3 left
		{time.Minute, "t3"},      // 2 left again
		{30 * time.Minute, "t4"}, // long after expiry
	}
	for i, s := range steps {
		clock = clock.Add(s.advance)
		tok, exp, err := c.Token(t.Context())
		if err != nil || tok != s.want || !exp.After(clock) {
			t.Errorf("step %d: Token = %q, %v, %v; want %q", i, tok, exp, err, s.want)
		}
	}
	if src.calls() != 4 {
		t.Errorf("source called %d times, want 4", src.calls())
	}
}

func TestCachedSourceNeverHandsOutATokenInsideTheMargin(t *testing.T) {
	clock := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	// A token that lives less than the margin is returned but not kept.
	src := &counting{ttl: time.Minute, clock: &clock}
	c := cached(src, 2*time.Minute, &clock)
	for i := range 3 {
		if tok, _, err := c.Token(t.Context()); err != nil || tok != fmt.Sprintf("t%d", i+1) {
			t.Errorf("call %d: Token = %q, %v", i, tok, err)
		}
	}
	// A token with an unknown expiry is not kept either.
	src = &counting{clock: &clock}
	c = cached(src, 0, &clock)
	for range 2 {
		if _, exp, err := c.Token(t.Context()); err != nil || !exp.IsZero() {
			t.Errorf("Token expiry = %v, %v", exp, err)
		}
	}
	if src.calls() != 2 {
		t.Errorf("source called %d times, want 2", src.calls())
	}
}

func TestCachedSourceDefaultMargin(t *testing.T) {
	clock := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	src := &counting{ttl: 10 * time.Minute, clock: &clock}
	c := cached(src, -1, &clock)
	if c.margin != DefaultRefreshMargin {
		t.Fatalf("margin = %v", c.margin)
	}
	_, _, _ = c.Token(t.Context())
	clock = clock.Add(10*time.Minute - DefaultRefreshMargin + time.Second)
	_, _, _ = c.Token(t.Context())
	if src.calls() != 2 {
		t.Errorf("source called %d times, want 2", src.calls())
	}
	if NewCachedSource(src, 0).now == nil {
		t.Error("no clock")
	}
}

func TestCachedSourceErrorsAreNotCachedOrServedStale(t *testing.T) {
	clock := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	boom := errors.New("boom")
	src := &counting{ttl: 15 * time.Minute, clock: &clock}
	c := cached(src, time.Minute, &clock)
	if tok, _, err := c.Token(t.Context()); err != nil || tok != "t1" {
		t.Fatalf("Token = %q, %v", tok, err)
	}
	clock = clock.Add(14*time.Minute + 30*time.Second) // inside the margin, still valid
	src.err = boom
	if tok, _, err := c.Token(t.Context()); !errors.Is(err, boom) || tok != "" {
		t.Errorf("Token = %q, %v; want the error and no stale token", tok, err)
	}
	src.err = nil
	if tok, _, err := c.Token(t.Context()); err != nil || tok != "t3" {
		t.Errorf("Token = %q, %v; want a fresh token after the failure", tok, err)
	}
	src.empty = true
	c.Invalidate()
	if _, _, err := c.Token(t.Context()); !errors.Is(err, ErrCredentialsUnavailable) {
		t.Errorf("an empty token = %v", err)
	}
}

func TestCachedSourceInvalidate(t *testing.T) {
	clock := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	src := &counting{ttl: time.Hour, clock: &clock}
	c := cached(src, time.Minute, &clock)
	_, _, _ = c.Token(t.Context())
	c.Invalidate()
	if tok, _, _ := c.Token(t.Context()); tok != "t2" {
		t.Errorf("after Invalidate Token = %q, want t2", tok)
	}
}

// Concurrent callers share one fetch and never see an error or a token they
// should not (run under -race).
func TestCachedSourceConcurrent(t *testing.T) {
	clock := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	src := &counting{ttl: time.Hour, clock: &clock}
	c := cached(src, time.Minute, &clock)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if tok, _, err := c.Token(t.Context()); err != nil || tok != "t1" {
				t.Errorf("Token = %q, %v", tok, err)
			}
		})
	}
	wg.Wait()
	if src.calls() != 1 {
		t.Errorf("source called %d times, want 1", src.calls())
	}
}

func TestTokenFunc(t *testing.T) {
	want := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	var s TokenSource = TokenFunc(func(context.Context) (string, time.Time, error) { return "x", want, nil })
	if tok, exp, err := s.Token(t.Context()); tok != "x" || !exp.Equal(want) || err != nil {
		t.Errorf("Token = %q, %v, %v", tok, exp, err)
	}
}
