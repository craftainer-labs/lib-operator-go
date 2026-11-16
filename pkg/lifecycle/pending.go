package lifecycle

import (
	"errors"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
)

// MinPendingInterval is the shortest requeue a PendingError asks for: a
// shorter or non-positive After is raised to it, so a buggy External cannot
// spin the work queue.
const MinPendingInterval = time.Second

// PendingError is what an External returns, from any method, when the cloud
// API accepted the request but the object is still changing state (ElastiCache
// and MemoryDB users, Memorystore long-running operations; ADR 0002, section
// 16.6). It is not a failure: Reconcile requeues after After, which may be
// much shorter than MinPollInterval, with no error and so no backoff, and sets
// Synced=True and Ready=False with PendingError.Reason. A PendingError can be
// wrapped; Reconcile finds it with errors.As.
type PendingError struct {
	// After is when to look again. It is clamped to MinPendingInterval at the
	// low end and to the claim's poll interval at the high end.
	After time.Duration
	// Reason is the Ready and Synced condition reason, typically
	// conditions.Creating, conditions.Updating or conditions.Deleting. Empty
	// means conditions.Reconciling.
	Reason string
	// Message is the condition message. It must not contain credentials. Empty
	// means "waiting for the backend to finish".
	Message string
}

// Pending returns a *PendingError.
func Pending(after time.Duration, reason, message string) error {
	return &PendingError{After: after, Reason: reason, Message: message}
}

// Error implements error.
func (e *PendingError) Error() string {
	if e.Message == "" {
		return "waiting for the backend to finish"
	}
	return e.Message
}

// pending records a PendingError: Ready=False with its reason, Synced=True (the
// request went through), and a requeue with no error.
func (r *run[S, O]) pending(action string, p *PendingError) (ctrl.Result, error) {
	reason := p.Reason
	if reason == "" {
		reason = conditions.Reconciling
	}
	after := min(max(p.After, MinPendingInterval), r.poll)
	msg := p.Error()
	r.set(conditions.Synced, metav1.ConditionTrue, reason, msg)
	r.set(conditions.Ready, metav1.ConditionFalse, reason, msg)
	return r.done(action, reason, ctrl.Result{RequeueAfter: after}, nil)
}

// asPending finds a PendingError in err's chain.
func asPending(err error) (*PendingError, bool) {
	var p *PendingError
	return p, errors.As(err, &p)
}
