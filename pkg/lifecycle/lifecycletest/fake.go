// Package lifecycletest is test support for operators built on
// pkg/lifecycle: a scriptable fake External and Rotator, so a controller's
// tests can drive Reconcile without a backend. The names follow the standard
// library's net/http/httptest convention.
//
// Fake simulates a backend with one external object: Create makes it, Update
// brings it up to date, Delete removes it, and every call is recorded. Set the
// Err fields to make a method fail, and Obs to start from an existing or
// drifted object.
package lifecycletest

import (
	"context"
	"sync"

	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle"
)

// Method names recorded in Fake.Calls.
const (
	CallObserve      = "Observe"
	CallCreate       = "Create"
	CallUpdate       = "Update"
	CallDelete       = "Delete"
	CallLateInit     = "LateInitialize"
	CallRotateBegin  = "RotateBegin"
	CallRotateFinish = "RotateFinish"
)

// Fake is a lifecycle.External[S, O] over one simulated external object. It is
// safe for concurrent use; set its fields before the first call.
type Fake[S, O any] struct {
	mu    sync.Mutex
	calls []string

	// Obs is the simulated object. Create sets Exists and UpToDate, Update sets
	// UpToDate, Delete clears Exists.
	Obs lifecycle.Observation[O]
	// CreateData and UpdateData are the binding data Create and Update return.
	CreateData, UpdateData binding.Data
	// ObserveErr, CreateErr, UpdateErr and DeleteErr make the method fail
	// without changing Obs.
	ObserveErr, CreateErr, UpdateErr, DeleteErr error
	// Created is the object Create stores in Obs.Current.
	Created O
}

var _ lifecycle.External[struct{}, struct{}] = (*Fake[struct{}, struct{}])(nil)

func (f *Fake[S, O]) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

// Calls returns the recorded method names in order.
func (f *Fake[S, O]) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Reset forgets the recorded calls.
func (f *Fake[S, O]) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

// Observe returns Obs, or ObserveErr.
func (f *Fake[S, O]) Observe(_ context.Context, _ S) (lifecycle.Observation[O], error) {
	f.record(CallObserve)
	if f.ObserveErr != nil {
		return lifecycle.Observation[O]{}, f.ObserveErr
	}
	return f.Obs, nil
}

// Create makes the object and returns CreateData, or fails with CreateErr.
func (f *Fake[S, O]) Create(_ context.Context, _ S) (binding.Data, error) {
	f.record(CallCreate)
	if f.CreateErr != nil {
		return nil, f.CreateErr
	}
	f.Obs = lifecycle.Observation[O]{Exists: true, UpToDate: true, Current: f.Created}
	return f.CreateData, nil
}

// Update brings the object up to date and returns UpdateData, or fails with
// UpdateErr.
func (f *Fake[S, O]) Update(_ context.Context, _ S, _ O) (binding.Data, error) {
	f.record(CallUpdate)
	if f.UpdateErr != nil {
		return nil, f.UpdateErr
	}
	f.Obs.UpToDate = true
	return f.UpdateData, nil
}

// Delete removes the object, or fails with DeleteErr.
func (f *Fake[S, O]) Delete(_ context.Context, _ O) error {
	f.record(CallDelete)
	if f.DeleteErr != nil {
		return f.DeleteErr
	}
	f.Obs.Exists = false
	return nil
}

// FakeRotator is a Fake that is also a lifecycle.Rotator. RotateBegin returns
// NextData and NextGeneration and records the RotationState it was given;
// RotateFinish records its state too.
type FakeRotator[S, O any] struct {
	Fake[S, O]

	// NextData and NextGeneration are what RotateBegin returns.
	NextData       binding.Data
	NextGeneration lifecycle.Generation
	// BeginErr and FinishErr make RotateBegin and RotateFinish fail.
	BeginErr, FinishErr error
	// HardCutBackend makes the fake a lifecycle.HardCutRotator reporting true.
	HardCutBackend bool

	begun, finished []lifecycle.RotationState
}

var _ lifecycle.Rotator[struct{}, struct{}] = (*FakeRotator[struct{}, struct{}])(nil)

// RotateBegin returns NextData and NextGeneration, or BeginErr.
func (f *FakeRotator[S, O]) RotateBegin(_ context.Context, _ S, _ O, st lifecycle.RotationState) (binding.Data, lifecycle.Generation, error) {
	f.record(CallRotateBegin)
	f.mu.Lock()
	f.begun = append(f.begun, st)
	f.mu.Unlock()
	if f.BeginErr != nil {
		return nil, "", f.BeginErr
	}
	return f.NextData, f.NextGeneration, nil
}

// RotateFinish succeeds, or fails with FinishErr.
func (f *FakeRotator[S, O]) RotateFinish(_ context.Context, _ S, _ O, st lifecycle.RotationState) error {
	f.record(CallRotateFinish)
	f.mu.Lock()
	f.finished = append(f.finished, st)
	f.mu.Unlock()
	return f.FinishErr
}

// HardCut implements lifecycle.HardCutRotator.
func (f *FakeRotator[S, O]) HardCut() bool { return f.HardCutBackend }

// BeginStates returns the RotationState of every RotateBegin call.
func (f *FakeRotator[S, O]) BeginStates() []lifecycle.RotationState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]lifecycle.RotationState(nil), f.begun...)
}

// FinishStates returns the RotationState of every RotateFinish call.
func (f *FakeRotator[S, O]) FinishStates() []lifecycle.RotationState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]lifecycle.RotationState(nil), f.finished...)
}

// LateInitializing is a FakeRotator-free Fake that also implements
// lifecycle.LateInitializer, returning Values or LateErr.
type LateInitializing[S, O any] struct {
	Fake[S, O]

	Values  map[string]string
	LateErr error
}

var _ lifecycle.LateInitializer[struct{}, struct{}] = (*LateInitializing[struct{}, struct{}])(nil)

// LateInitialize returns Values, or LateErr.
func (f *LateInitializing[S, O]) LateInitialize(_ context.Context, _ S, _ O) (map[string]string, error) {
	f.record(CallLateInit)
	return f.Values, f.LateErr
}
