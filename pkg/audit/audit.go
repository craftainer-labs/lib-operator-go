// Package audit writes the structured audit log of ADR 0002 section 11: who,
// what, when, source, and before and after as the names of the changed spec
// fields plus SHA-256 sums of the old and new spec, never the values.
// Credentials, tokens and Secret data never reach it.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Message is the log message of every audit line.
const Message = "audit"

// Entry is one audit record.
type Entry struct {
	// Controller is the operator's name; LastManager is the last managedFields
	// manager of the claim (the user-driven change).
	Controller, LastManager string
	Kind, Namespace, Name   string
	UID                     types.UID
	Generation              int64
	ExternalName            string
	// Action is what was done (Create, Update, Delete, Rotate, Observe) and
	// Result is "success", or the failure reason.
	Action, Result string
	// ServerRef is "<kind>/<name>" and ServerHost the endpoint's host.
	ServerRef, ServerHost string
	// ChangedFields are the JSON names of the spec fields that differ from the
	// last reconcile. PreviousUnknown is true when the process had not seen the
	// object before (after a restart): there is then no "before", and
	// OldSpecSHA256 is empty.
	ChangedFields                []string
	PreviousUnknown              bool
	OldSpecSHA256, NewSpecSHA256 string
}

// Log writes the entry through the logger in ctx, as JSON when that logger is.
func Log(ctx context.Context, e Entry) {
	log.FromContext(ctx).Info(Message,
		"controller", e.Controller,
		"lastManager", e.LastManager,
		"kind", e.Kind,
		"namespace", e.Namespace,
		"name", e.Name,
		"uid", string(e.UID),
		"generation", e.Generation,
		"externalName", e.ExternalName,
		"action", e.Action,
		"result", e.Result,
		"serverRef", e.ServerRef,
		"serverHost", e.ServerHost,
		"changedFields", e.ChangedFields,
		"previousUnknown", e.PreviousUnknown,
		"oldSpecSHA256", e.OldSpecSHA256,
		"newSpecSHA256", e.NewSpecSHA256,
	)
}

// LastManager returns the manager of the most recent managedFields entry of
// the object, or "" when it has none.
func LastManager(obj client.Object) string {
	var manager string
	var latest int64
	for _, f := range obj.GetManagedFields() {
		if f.Time != nil && f.Time.UnixNano() >= latest {
			latest, manager = f.Time.UnixNano(), f.Manager
		}
	}
	return manager
}

// Tracker remembers, per object UID, what each top-level spec field looked
// like at the last reconcile, in memory: only hashes, never values. After a
// restart the first Observe of an object has no "before". It also remembers,
// per object, the last value of a named notice, so a recurring warning is
// raised only when its value changes.
type Tracker struct {
	mu      sync.Mutex
	seen    map[types.UID]snapshot
	notices map[types.UID]map[string]string
}

type snapshot struct {
	sum    string
	fields map[string]string
}

// NewTracker returns an empty Tracker.
func NewTracker() *Tracker {
	return &Tracker{seen: map[types.UID]snapshot{}, notices: map[types.UID]map[string]string{}}
}

// Observe records the spec of the object and returns the sorted names of the
// top-level fields that changed since the last call for the same UID, and the
// SHA-256 sums of the previous spec and of this one. On the first call for a
// UID there is no previous spec: changed is nil and oldSum is empty ("previous
// unknown"), rather than every field being reported as changed.
func (t *Tracker) Observe(uid types.UID, spec any) (changed []string, oldSum, newSum string, err error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, "", "", fmt.Errorf("marshal spec: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, "", "", fmt.Errorf("spec is not an object: %w", err)
	}
	cur := snapshot{sum: sum(raw), fields: make(map[string]string, len(fields))}
	for k, v := range fields {
		cur.fields[k] = sum(v)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	prev, had := t.seen[uid]
	t.seen[uid] = cur
	if !had {
		return nil, "", cur.sum, nil
	}
	for k, v := range cur.fields {
		if prev.fields[k] != v {
			changed = append(changed, k)
		}
	}
	for k := range prev.fields {
		if _, ok := cur.fields[k]; !ok {
			changed = append(changed, k)
		}
	}
	slices.Sort(changed)
	return changed, prev.sum, cur.sum, nil
}

// Forget drops what Tracker remembers about the object, once it is gone.
func (t *Tracker) Forget(uid types.UID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.seen, uid)
	delete(t.notices, uid)
}

// Notice reports whether value differs from the last value noticed for key on
// the object (true the first time), and remembers it.
func (t *Tracker) Notice(uid types.UID, key, value string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	last, had := t.notices[uid][key]
	if t.notices[uid] == nil {
		t.notices[uid] = map[string]string{}
	}
	t.notices[uid][key] = value
	return !had || last != value
}

// Clear forgets the notice for key, so the next Notice of any value is new.
func (t *Tracker) Clear(uid types.UID, key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.notices[uid], key)
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
