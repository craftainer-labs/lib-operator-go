package lifecycletest_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle/lifecycletest"
)

type (
	spec struct{}
	obj  string
)

func TestFakeSimulatesAnExternalObject(t *testing.T) {
	ctx := context.Background()
	f := &lifecycletest.Fake[spec, obj]{
		Created:    "created",
		CreateData: binding.Data{"host": []byte("db")},
		UpdateData: binding.Data{"host": []byte("db2")},
	}

	obs, err := f.Observe(ctx, spec{})
	if err != nil || obs.Exists {
		t.Fatalf("Observe = %+v, %v", obs, err)
	}
	data, err := f.Create(ctx, spec{})
	if err != nil || string(data["host"]) != "db" {
		t.Fatalf("Create = %v, %v", data, err)
	}
	if obs, _ = f.Observe(ctx, spec{}); !obs.Exists || !obs.UpToDate || obs.Current != "created" {
		t.Errorf("after Create: %+v", obs)
	}

	f.Obs.UpToDate = false
	if data, err = f.Update(ctx, spec{}, "created"); err != nil || string(data["host"]) != "db2" {
		t.Fatalf("Update = %v, %v", data, err)
	}
	if obs, _ = f.Observe(ctx, spec{}); !obs.UpToDate {
		t.Errorf("after Update: %+v", obs)
	}

	if err = f.Delete(ctx, "created"); err != nil {
		t.Fatal(err)
	}
	if obs, _ = f.Observe(ctx, spec{}); obs.Exists {
		t.Errorf("after Delete: %+v", obs)
	}

	want := []string{"Observe", "Create", "Observe", "Update", "Observe", "Delete", "Observe"}
	if got := f.Calls(); !slices.Equal(got, want) {
		t.Errorf("Calls = %v, want %v", got, want)
	}
	f.Reset()
	if len(f.Calls()) != 0 {
		t.Error("Reset kept the calls")
	}
}

func TestFakeFailuresLeaveTheObjectAlone(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	f := &lifecycletest.Fake[spec, obj]{ObserveErr: boom, CreateErr: boom, UpdateErr: boom, DeleteErr: boom,
		Obs: lifecycle.Observation[obj]{Exists: true}}

	if _, err := f.Observe(ctx, spec{}); !errors.Is(err, boom) {
		t.Errorf("Observe: %v", err)
	}
	if _, err := f.Create(ctx, spec{}); !errors.Is(err, boom) {
		t.Errorf("Create: %v", err)
	}
	if _, err := f.Update(ctx, spec{}, ""); !errors.Is(err, boom) {
		t.Errorf("Update: %v", err)
	}
	if err := f.Delete(ctx, ""); !errors.Is(err, boom) {
		t.Errorf("Delete: %v", err)
	}
	if !f.Obs.Exists || f.Obs.UpToDate {
		t.Errorf("a failed call changed the object: %+v", f.Obs)
	}
}

func TestFakeRotator(t *testing.T) {
	ctx := context.Background()
	r := &lifecycletest.FakeRotator[spec, obj]{NextData: binding.Data{"password": []byte("new")}, NextGeneration: "b"}

	var _ lifecycle.HardCutRotator = r
	if r.HardCut() {
		t.Error("HardCut defaults to false")
	}
	r.HardCutBackend = true
	if !r.HardCut() {
		t.Error("HardCutBackend not reported")
	}

	st := lifecycle.RotationState{Active: "a", Sequence: 1}
	data, gen, err := r.RotateBegin(ctx, spec{}, "cur", st)
	if err != nil || gen != "b" || string(data["password"]) != "new" {
		t.Fatalf("RotateBegin = %v, %q, %v", data, gen, err)
	}
	if err := r.RotateFinish(ctx, spec{}, "cur", lifecycle.RotationState{Active: "b", Previous: "a", Sequence: 2}); err != nil {
		t.Fatal(err)
	}
	if got := r.BeginStates(); len(got) != 1 || got[0] != st {
		t.Errorf("BeginStates = %+v", got)
	}
	if got := r.FinishStates(); len(got) != 1 || got[0].Previous != "a" {
		t.Errorf("FinishStates = %+v", got)
	}
	if got := r.Calls(); !slices.Equal(got, []string{"RotateBegin", "RotateFinish"}) {
		t.Errorf("Calls = %v", got)
	}

	boom := errors.New("boom")
	r.BeginErr, r.FinishErr = boom, boom
	if _, _, err := r.RotateBegin(ctx, spec{}, "", lifecycle.RotationState{}); !errors.Is(err, boom) {
		t.Errorf("RotateBegin: %v", err)
	}
	if err := r.RotateFinish(ctx, spec{}, "", lifecycle.RotationState{}); !errors.Is(err, boom) {
		t.Errorf("RotateFinish: %v", err)
	}
}

func TestLateInitializing(t *testing.T) {
	f := &lifecycletest.LateInitializing[spec, obj]{Values: map[string]string{"region": "eu"}}
	got, err := f.LateInitialize(context.Background(), spec{}, "cur")
	if err != nil || got["region"] != "eu" {
		t.Fatalf("LateInitialize = %v, %v", got, err)
	}
	boom := errors.New("boom")
	f.LateErr = boom
	if _, err := f.LateInitialize(context.Background(), spec{}, "cur"); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	if got := f.Calls(); !slices.Equal(got, []string{"LateInitialize", "LateInitialize"}) {
		t.Errorf("Calls = %v", got)
	}
}
