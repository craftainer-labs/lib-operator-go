package lifecycle_test

import (
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle"
)

func TestParseDefaultDeletionPolicy(t *testing.T) {
	tests := []struct {
		in      string
		want    common.DeletionPolicy
		wantErr bool
	}{
		{"", common.DeletionRetain, false},
		{"Retain", common.DeletionRetain, false},
		{"Delete", common.DeletionDelete, false},
		{"delete", "", true},
		{"Orphan", "", true},
		{" Retain", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := lifecycle.ParseDefaultDeletionPolicy(tt.in)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("got %q, %v; want %q, error %v", got, err, tt.want, tt.wantErr)
			}
			if err != nil {
				for _, s := range []string{lifecycle.EnvDefaultDataDeletionPolicy, tt.in, "Retain", "Delete"} {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q does not mention %q", err, s)
					}
				}
			}
		})
	}
}

func TestDefaultDeletionPolicyFromEnv(t *testing.T) {
	run := func(value string) (common.DeletionPolicy, []string, []int) {
		var lines []string
		var exits []int
		log := funcr.NewJSON(func(l string) { lines = append(lines, l) }, funcr.Options{})
		got := lifecycle.DefaultDeletionPolicyFromEnv(log, func(k string) string {
			if k != lifecycle.EnvDefaultDataDeletionPolicy {
				t.Errorf("read %q", k)
			}
			return value
		}, func(code int) { exits = append(exits, code) })
		return got, lines, exits
	}

	for in, want := range map[string]common.DeletionPolicy{"": common.DeletionRetain, "Retain": common.DeletionRetain, "Delete": common.DeletionDelete} {
		got, lines, exits := run(in)
		if got != want || len(exits) != 0 || len(lines) != 1 || !strings.Contains(lines[0], `"deletionPolicy":"`+string(want)+`"`) {
			t.Errorf("%q: got %q, logs %v, exits %v", in, got, lines, exits)
		}
	}

	got, lines, exits := run("Maybe")
	if got != "" || len(exits) != 1 || exits[0] != 1 || len(lines) != 1 ||
		!strings.Contains(lines[0], "Maybe") || !strings.Contains(lines[0], "allowed values") {
		t.Errorf("invalid: got %q, logs %v, exits %v", got, lines, exits)
	}
}

func TestPredicates(t *testing.T) {
	p := lifecycle.Predicates()
	base := func(mut ...func(*testWidget)) *testWidget { return widget(mut...) }
	update := func(o, n client.Object) bool {
		return p.Update(event.UpdateEvent{ObjectOld: o, ObjectNew: n})
	}
	annotated := func(k, v string) func(*testWidget) {
		return func(w *testWidget) { w.Annotations = map[string]string{k: v} }
	}
	deleting := func(w *testWidget) {
		ts := metav1.Now()
		w.DeletionTimestamp = &ts
	}

	if !update(base(), base(func(w *testWidget) { w.Generation = 2 })) {
		t.Error("a spec change (generation) must trigger")
	}
	if update(base(), base(func(w *testWidget) { w.Status.ExternalName = "x" })) {
		t.Error("a status write must not trigger")
	}
	if update(base(), base(func(w *testWidget) { w.Labels = map[string]string{"a": "b"} })) {
		t.Error("an unrelated metadata change must not trigger")
	}
	for _, a := range []string{lifecycle.AnnotationPollInterval, lifecycle.AnnotationRotate, lifecycle.AnnotationPaused} {
		if !update(base(), base(annotated(a, "v"))) {
			t.Errorf("a change of %s must trigger", a)
		}
		if update(base(annotated(a, "v")), base(annotated(a, "v"))) {
			t.Errorf("an unchanged %s must not trigger", a)
		}
	}
	if update(base(), base(annotated("unrelated", "v"))) {
		t.Error("an unrelated annotation must not trigger")
	}
	if !update(base(), base(deleting)) {
		t.Error("a deletion must trigger")
	}
	if !p.Create(event.CreateEvent{Object: base()}) || !p.Delete(event.DeleteEvent{Object: base()}) || !p.Generic(event.GenericEvent{Object: base()}) {
		t.Error("create, delete and generic events must trigger")
	}
}
