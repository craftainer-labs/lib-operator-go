package dcr_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
	"github.com/craftainer-labs/lib-operator-go/pkg/dcr"
)

const opNS = "craftainer-system"

func claim() *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "web", UID: "uid-1"}}
}

func store(t *testing.T, funcs *interceptor.Funcs) (dcr.Store, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	b := fake.NewClientBuilder().WithScheme(s)
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	c := b.Build()
	return dcr.Store{Client: c, Namespace: opNS, ManagedBy: "operator-oidc"}, c
}

func TestWriteReadRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, c := store(t, nil)

	if _, err := st.Read(ctx, claim()); !errors.Is(err, dcr.ErrNotFound) {
		t.Fatalf("Read before Write: err = %v", err)
	}
	if err := st.Write(ctx, claim(), dcr.State{ClientID: "c1", RegistrationClientURI: "https://idp/reg/c1", RegistrationAccessToken: "rat-1"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Read(ctx, claim())
	if err != nil || got.ClientID != "c1" || got.RegistrationClientURI != "https://idp/reg/c1" || got.RegistrationAccessToken != "rat-1" {
		t.Fatalf("Read = %+v, %v", got, err)
	}

	var s corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: opNS, Name: "dcr-uid-1"}, &s); err != nil {
		t.Fatal(err)
	}
	wantLabels := map[string]string{
		binding.LabelManagedBy: "operator-oidc", dcr.LabelClaimNamespace: "team-a", dcr.LabelClaimName: "web", dcr.LabelClaimUID: "uid-1",
	}
	for k, v := range wantLabels {
		if s.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, s.Labels[k], v)
		}
	}
	if len(s.OwnerReferences) != 0 {
		t.Error("a cross-namespace owner reference is not allowed")
	}

	// RFC 7592 may return a new token after a PUT: Write again replaces it.
	if err := st.Write(ctx, claim(), dcr.State{ClientID: "c1", RegistrationClientURI: "https://idp/reg/c1", RegistrationAccessToken: "rat-2"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Read(ctx, claim()); got.RegistrationAccessToken != "rat-2" {
		t.Errorf("token after rewrite = %q", got.RegistrationAccessToken)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st, _ := store(t, nil)
	if err := st.Delete(ctx, claim()); err != nil {
		t.Errorf("Delete of nothing: %v", err)
	}
	if err := st.Write(ctx, claim(), dcr.State{ClientID: "c1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ctx, claim()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Read(ctx, claim()); !errors.Is(err, dcr.ErrNotFound) {
		t.Errorf("Read after Delete: %v", err)
	}
}

func TestOrphanKeepsTheStateAndLabelsIt(t *testing.T) {
	ctx := context.Background()
	st, c := store(t, nil)
	if err := st.Orphan(ctx, claim()); !errors.Is(err, dcr.ErrNotFound) {
		t.Errorf("Orphan of nothing: %v", err)
	}
	if err := st.Write(ctx, claim(), dcr.State{ClientID: "c1", RegistrationAccessToken: "rat"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Orphan(ctx, claim()); err != nil {
		t.Fatal(err)
	}
	var s corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: opNS, Name: dcr.SecretName("uid-1")}, &s); err != nil {
		t.Fatal(err)
	}
	if s.Labels[dcr.LabelOrphaned] != "true" || string(s.Data[dcr.KeyRegistrationToken]) != "rat" {
		t.Errorf("secret = labels %v data keys %d", s.Labels, len(s.Data))
	}
	// Orphan on a Secret with no labels at all.
	s.Labels = nil
	if err := c.Update(ctx, &s); err != nil {
		t.Fatal(err)
	}
	if err := st.Orphan(ctx, claim()); err != nil {
		t.Fatal(err)
	}
}

func TestStateNeverPrintsTheToken(t *testing.T) {
	st := dcr.State{ClientID: "c1", RegistrationClientURI: "https://idp/reg/c1", RegistrationAccessToken: "rat-secret"}
	for _, s := range []string{fmt.Sprint(st), fmt.Sprintf("%+v", st), fmt.Sprintf("%#v", st)} {
		if strings.Contains(s, "rat-secret") || !strings.Contains(s, "c1") {
			t.Errorf("formatted State = %q", s)
		}
	}
}

func TestStoreErrorsAreReturned(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	st, _ := store(t, &interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return boom
		},
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error { return boom },
	})
	if _, err := st.Read(ctx, claim()); !errors.Is(err, boom) || errors.Is(err, dcr.ErrNotFound) {
		t.Errorf("Read: %v", err)
	}
	if err := st.Orphan(ctx, claim()); !errors.Is(err, boom) || errors.Is(err, dcr.ErrNotFound) {
		t.Errorf("Orphan: %v", err)
	}
	if err := st.Write(ctx, claim(), dcr.State{}); !errors.Is(err, boom) {
		t.Errorf("Write: %v", err)
	}
}
