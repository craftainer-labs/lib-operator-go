package lifecycle

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/audit"
	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
)

// EnvDefaultDataDeletionPolicy is the environment variable every operator reads
// for the default deletionPolicy of its data kinds.
const EnvDefaultDataDeletionPolicy = "DEFAULT_DATA_DELETION_POLICY"

// Poll interval bounds and the default (ADR 0002, section 6).
const (
	DefaultPollInterval = 10 * time.Minute
	MinPollInterval     = 30 * time.Second
	MaxPollInterval     = 24 * time.Hour
	// jitterFraction is the largest share added to a poll interval.
	jitterFraction = 0.1
)

// Class tells Reconcile which kind of claim it reconciles.
type Class int

const (
	// ClassData is a data kind (Database, Bucket): it has spec.deletionPolicy and
	// the pinned status.effectiveDeletionPolicy, and no binding Secret.
	ClassData Class = iota + 1
	// ClassCredential is a credential-bearing kind: it is always deleted with
	// its claim (when managementPolicies permits Delete), writes a binding Secret
	// and may rotate.
	ClassCredential
)

// ErrInvalidOptions means Options cannot be used to reconcile.
var ErrInvalidOptions = errors.New("invalid lifecycle options")

// Options configure Reconcile for one controller.
type Options struct {
	// Class is required.
	Class Class
	// BindingType is <type> in servicebinding.io/<type> and Provider the
	// provider key; both are required for ClassCredential.
	BindingType, Provider string
	// ManagedBy is the operator's name, the managed-by label of binding Secrets.
	ManagedBy string
	// FinalizerName defaults to DefaultFinalizer.
	FinalizerName string
	// DefaultPollInterval defaults to 10m; the craftainer.dev/poll-interval
	// annotation overrides it per resource.
	DefaultPollInterval time.Duration
	// DefaultDeletionPolicy is the operator's default for data kinds, usually from
	// DefaultDeletionPolicyFromEnv. Empty means Retain.
	DefaultDeletionPolicy common.DeletionPolicy
	// Recorder emits the Events. Required.
	Recorder events.EventRecorder
	// ResolveErr is the error server.Resolve returned for this claim, or nil. When
	// it is set the External may be nil.
	ResolveErr error
	// APIReader is the manager's uncached reader (mgr.GetAPIReader()). The
	// binding Secret is looked for through it, because the operator's Secret
	// cache is restricted to its managed-by label and cannot see a user's
	// Secret of the same name. nil uses the client.
	APIReader client.Reader
	// ServerHost is the host of the server's endpoint, for the audit log.
	ServerHost string
	// OnOrphan, if set, runs when the external object is left in place because
	// managementPolicies lacks Delete. operator-oidc uses it to relabel the DCR
	// state Secret (dcr.Store.Orphan). An error blocks the deletion.
	OnOrphan func(ctx context.Context, claim Claim) error
	// Audit remembers the last reconciled spec per claim for the audit log; nil
	// uses one shared by the process.
	Audit *audit.Tracker
	// Now defaults to time.Now; Jitter returns a number in [0, 1) and defaults
	// to a crypto/rand draw. Both exist for tests.
	Now    func() time.Time
	Jitter func() float64
}

var sharedTracker = audit.NewTracker()

func (o Options) resolve() (Options, error) {
	switch {
	case o.Class != ClassData && o.Class != ClassCredential:
		return o, fmt.Errorf("%w: Class is required", ErrInvalidOptions)
	case o.Recorder == nil:
		return o, fmt.Errorf("%w: Recorder is required", ErrInvalidOptions)
	case o.Class == ClassCredential && (o.BindingType == "" || o.Provider == ""):
		return o, fmt.Errorf("%w: BindingType and Provider are required for a credential kind", ErrInvalidOptions)
	case o.DefaultDeletionPolicy != "" && o.DefaultDeletionPolicy != common.DeletionDelete &&
		o.DefaultDeletionPolicy != common.DeletionRetain:
		return o, fmt.Errorf("%w: DefaultDeletionPolicy %q", ErrInvalidOptions, o.DefaultDeletionPolicy)
	}
	if o.ManagedBy == "" {
		o.ManagedBy = binding.DefaultManagedBy
	}
	if o.FinalizerName == "" {
		o.FinalizerName = DefaultFinalizer
	}
	if o.DefaultPollInterval <= 0 {
		o.DefaultPollInterval = DefaultPollInterval
	}
	if o.DefaultDeletionPolicy == "" {
		o.DefaultDeletionPolicy = common.DeletionRetain
	}
	if o.Audit == nil {
		o.Audit = sharedTracker
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Jitter == nil {
		o.Jitter = cryptoJitter
	}
	return o, nil
}

func cryptoJitter() float64 {
	var b [8]byte
	_, _ = rand.Read(b[:]) // never fails since Go 1.24
	return float64(binary.BigEndian.Uint64(b[:])>>11) / (1 << 53)
}

// ParseDefaultDeletionPolicy parses DEFAULT_DATA_DELETION_POLICY: "" is Retain,
// "Retain" and "Delete" are themselves, anything else is an error.
func ParseDefaultDeletionPolicy(v string) (common.DeletionPolicy, error) {
	switch common.DeletionPolicy(v) {
	case "", common.DeletionRetain:
		return common.DeletionRetain, nil
	case common.DeletionDelete:
		return common.DeletionDelete, nil
	default:
		return "", fmt.Errorf("%s=%q: allowed values are %q and %q (unset or empty means %q)",
			EnvDefaultDataDeletionPolicy, v, common.DeletionRetain, common.DeletionDelete, common.DeletionRetain)
	}
}

// DefaultDeletionPolicyFromEnv reads DEFAULT_DATA_DELETION_POLICY through
// getenv (os.Getenv in main), logs the configured default once, and returns
// it. An invalid value is logged with the allowed values and exit(1) is called,
// so the operator never runs with a policy nobody chose; main passes os.Exit.
// If exit returns (in a test), the result is empty.
func DefaultDeletionPolicyFromEnv(log logr.Logger, getenv func(string) string, exit func(int)) common.DeletionPolicy {
	p, err := ParseDefaultDeletionPolicy(getenv(EnvDefaultDataDeletionPolicy))
	if err != nil {
		log.Error(err, "invalid operator configuration")
		exit(1)
		return ""
	}
	log.Info("default deletion policy for data kinds", "env", EnvDefaultDataDeletionPolicy, "deletionPolicy", string(p))
	return p
}

// Predicates is the event filter of a claim controller: a spec change
// (metadata.generation), a change of one of the three craftainer.dev
// annotations, or a deletion timestamp triggers a reconcile; status writes do
// not (ADR 0002, section 6). Add the owned Secret's watch separately.
func Predicates() predicate.Predicate {
	return predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
			o, n := e.ObjectOld, e.ObjectNew
			if o.GetDeletionTimestamp().IsZero() != n.GetDeletionTimestamp().IsZero() {
				return true
			}
			for _, a := range []string{AnnotationPollInterval, AnnotationRotate, AnnotationPaused} {
				if o.GetAnnotations()[a] != n.GetAnnotations()[a] {
					return true
				}
			}
			return false
		}},
	)
}
