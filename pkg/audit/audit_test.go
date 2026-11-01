package audit_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/craftainer-labs/lib-operator-go/pkg/audit"
)

type spec struct {
	Size  string `json:"size,omitempty"`
	Count int    `json:"count,omitempty"`
}

func TestTrackerReportsChangedFieldsAndSums(t *testing.T) {
	tr := audit.NewTracker()

	changed, oldSum, newSum, err := tr.Observe("uid", spec{Size: "s", Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if changed != nil || oldSum != "" || len(newSum) != 64 {
		t.Errorf("first sight: %v %q %q; want no previous spec rather than every field changed", changed, oldSum, newSum)
	}

	changed, old2, new2, err := tr.Observe("uid", spec{Size: "s", Count: 1})
	if err != nil || len(changed) != 0 || old2 != newSum || new2 != newSum {
		t.Errorf("unchanged: %v %q %q %v", changed, old2, new2, err)
	}

	changed, old3, new3, err := tr.Observe("uid", spec{Size: "m", Count: 1})
	if err != nil || !slices.Equal(changed, []string{"size"}) || old3 != newSum || new3 == newSum {
		t.Errorf("changed: %v %q %q %v", changed, old3, new3, err)
	}

	// A field that disappears counts as changed; values are never reported.
	changed, _, _, err = tr.Observe("uid", spec{Size: "m"})
	if err != nil || !slices.Equal(changed, []string{"count"}) {
		t.Errorf("removed field: %v %v", changed, err)
	}

	// Another object is independent; Forget drops what is remembered.
	if _, old, _, _ := tr.Observe("other", spec{}); old != "" {
		t.Errorf("another UID inherited state: %q", old)
	}
	tr.Forget("uid")
	if _, old, _, _ := tr.Observe("uid", spec{Size: "m"}); old != "" {
		t.Errorf("after Forget: %q", old)
	}
}

func TestTrackerNotices(t *testing.T) {
	tr := audit.NewTracker()
	if !tr.Notice("uid", "poll", "soon") {
		t.Error("the first value is new")
	}
	if tr.Notice("uid", "poll", "soon") {
		t.Error("the same value is not new")
	}
	if !tr.Notice("uid", "poll", "later") {
		t.Error("a changed value is new")
	}
	if !tr.Notice("other", "poll", "later") || !tr.Notice("uid", "another", "later") {
		t.Error("notices are per object and per key")
	}
	tr.Clear("uid", "poll")
	if !tr.Notice("uid", "poll", "later") {
		t.Error("after Clear the value is new again")
	}
	tr.Forget("uid")
	if !tr.Notice("uid", "poll", "later") {
		t.Error("after Forget the value is new again")
	}
	tr.Clear("nobody", "poll") // clearing what was never noticed is fine
}

func TestTrackerErrors(t *testing.T) {
	tr := audit.NewTracker()
	if _, _, _, err := tr.Observe("uid", make(chan int)); err == nil {
		t.Error("expected a marshal error")
	}
	if _, _, _, err := tr.Observe("uid", "not an object"); err == nil {
		t.Error("expected an error for a spec that is not an object")
	}
}

func TestTrackerIsSafeForConcurrentUse(*testing.T) {
	tr := audit.NewTracker()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, _ = tr.Observe("uid", spec{Count: i})
			tr.Forget("uid")
		}()
	}
	wg.Wait()
}

func TestLastManager(t *testing.T) {
	t0 := metav1.NewTime(time.Unix(1000, 0))
	t1 := metav1.NewTime(time.Unix(2000, 0))
	obj := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{ManagedFields: []metav1.ManagedFieldsEntry{
		{Manager: "kubectl", Time: &t1},
		{Manager: "helm", Time: &t0},
		{Manager: "no-time"},
	}}}
	if got := audit.LastManager(obj); got != "kubectl" {
		t.Errorf("LastManager = %q, want kubectl", got)
	}
	if got := audit.LastManager(&metav1.PartialObjectMetadata{}); got != "" {
		t.Errorf("LastManager of nothing = %q", got)
	}
}

func TestLogWritesTheEntry(t *testing.T) {
	var lines []string
	logger := funcr.NewJSON(func(obj string) { lines = append(lines, obj) }, funcr.Options{})
	ctx := log.IntoContext(context.Background(), logger)

	audit.Log(ctx, audit.Entry{
		Controller: "operator-db", LastManager: "kubectl", Kind: "Database", Namespace: "team-a", Name: "orders",
		UID: "uid-1", Generation: 4, ExternalName: "team-a_orders_x", Action: "Create", Result: "success",
		ServerRef: "ClusterDatabaseServer/shared", ServerHost: "pg.example", ChangedFields: []string{"extensions"}, PreviousUnknown: true,
		OldSpecSHA256: "old", NewSpecSHA256: "new",
	})
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	for _, want := range []string{`"msg":"audit"`, `"controller":"operator-db"`, `"lastManager":"kubectl"`, `"kind":"Database"`,
		`"uid":"uid-1"`, `"generation":4`, `"action":"Create"`, `"serverHost":"pg.example"`,
		`"changedFields":["extensions"]`, `"previousUnknown":true`,`"oldSpecSHA256":"old"`, `"newSpecSHA256":"new"`} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("log line lacks %s: %s", want, lines[0])
		}
	}
}
