package testenv

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
)

// RecordedEvent is one Event a Recorder saw.
type RecordedEvent struct {
	Type, Reason, Action, Note string
	// Regarding is the object the Event is about.
	Regarding runtime.Object
}

// Recorder is an events.EventRecorder that keeps the Events in memory, for
// assertions. Pass it as lifecycle.Options.Recorder.
type Recorder struct {
	mu     sync.Mutex
	events []RecordedEvent
}

var _ events.EventRecorder = (*Recorder)(nil)

// Eventf records the Event. The note is formatted with args only when there are
// any, like the real recorder.
func (r *Recorder) Eventf(regarding, _ runtime.Object, eventType, reason, action, note string, args ...any) {
	if len(args) > 0 { // a note without arguments is not a format: keep a literal % as it is
		note = fmt.Sprintf(note, args...)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, RecordedEvent{
		Type: eventType, Reason: reason, Action: action, Note: note, Regarding: regarding,
	})
}

// Events returns a copy of the recorded Events in order.
func (r *Recorder) Events() []RecordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RecordedEvent(nil), r.events...)
}

// Reasons returns the reasons of the recorded Events in order.
func (r *Recorder) Reasons() []string {
	var out []string
	for _, e := range r.Events() {
		out = append(out, e.Reason)
	}
	return out
}

// Reset forgets the recorded Events.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

// AssertEvent fails the test unless an Event of that type and reason was
// recorded. A non-empty noteContains must also appear in its note.
func (r *Recorder) AssertEvent(t testing.TB, eventType, reason, noteContains string) {
	t.Helper()
	for _, e := range r.Events() {
		if e.Type == eventType && e.Reason == reason && strings.Contains(e.Note, noteContains) {
			return
		}
	}
	t.Errorf("no %s Event with reason %q containing %q; recorded: %v", eventType, reason, noteContains, r.Reasons())
}

// AssertNoEvent fails the test if an Event with the reason was recorded.
func (r *Recorder) AssertNoEvent(t testing.TB, reason string) {
	t.Helper()
	for _, e := range r.Events() {
		if e.Reason == reason {
			t.Errorf("unexpected %s Event with reason %q: %s", e.Type, reason, e.Note)
		}
	}
}

// AssertReasons fails the test unless the recorded reasons are exactly these,
// in this order.
func (r *Recorder) AssertReasons(t testing.TB, want ...string) {
	t.Helper()
	if got := r.Reasons(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Event reasons = %v, want %v", got, want)
	}
}

// AssertNoSecret fails the test if any value appears in an Event's note. Use it
// with the values of a binding Secret: credentials never go into Events (ADR
// 0002, section 11).
func (r *Recorder) AssertNoSecret(t testing.TB, values ...string) {
	t.Helper()
	for _, e := range r.Events() {
		for _, v := range values {
			if v != "" && strings.Contains(e.Note, v) {
				t.Errorf("Event %s leaks a secret value in its note", e.Reason)
			}
		}
	}
}
