package binding_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
	"github.com/craftainer-labs/lib-operator-go/pkg/internal/testapi"
)

func TestSecretType(t *testing.T) {
	if got, want := string(binding.SecretType("postgresql")), "servicebinding.io/postgresql"; got != want {
		t.Fatalf("SecretType = %q, want %q", got, want)
	}
}

func TestKeys(t *testing.T) {
	want := map[string][]string{
		binding.TypePostgreSQL: {"host", "port", "database", "username", "password", "sslmode", "sslrootcert", "uri"},
		binding.TypeRedis:      {"host", "port", "username", "password", "uri", "keyPrefix", "sslrootcert"},
		binding.TypeS3:         {"endpoint", "region", "bucket", "accessKeyId", "secretAccessKey", "prefix", "pathStyle", "sslrootcert"},
		binding.TypeMQTT:       {"host", "port", "protocol", "username", "password", "clientId", "topicRoot", "sslrootcert"},
		binding.TypeOIDC:       {"issuer", "clientId", "clientSecret", "tokenEndpoint", "authorizationEndpoint", "jwksUri", "userinfoEndpoint"},
	}
	for typ, keys := range want {
		got, ok := binding.Keys(typ)
		if !ok || strings.Join(got, ",") != strings.Join(keys, ",") {
			t.Errorf("Keys(%s) = %v, %v; want %v", typ, got, ok, keys)
		}
		got[0] = "mutated"
		if again, _ := binding.Keys(typ); again[0] == "mutated" {
			t.Errorf("Keys(%s) returns the shared slice", typ)
		}
	}
	if _, ok := binding.Keys("nosuch"); ok {
		t.Error("Keys reports an unknown type as known")
	}
}

func TestDataNeverPrintsValues(t *testing.T) {
	d := binding.Data{"password": []byte("hunter2"), "host": []byte("db")}
	if got := fmt.Sprint(d); got != "binding.Data{host,password}" {
		t.Errorf("String = %q", got)
	}
	for _, s := range []string{fmt.Sprintf("%v", d), fmt.Sprintf("%q", d), fmt.Sprintf("%#v", d)} {
		if strings.Contains(s, "hunter2") || strings.Contains(s, "aHVudGVyMg") {
			t.Errorf("formatted Data leaks a value: %q", s)
		}
	}
	if got := d.Keys(); strings.Join(got, ",") != "host,password" {
		t.Errorf("Keys = %v", got)
	}
}

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := testapi.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func owner(name string, uid types.UID) *testapi.Gadget {
	return &testapi.Gadget{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: uid}}
}

func getSecret(t *testing.T, c client.Client, name string) *corev1.Secret {
	t.Helper()
	var s corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func TestWriteSecretCreatesAndUpdates(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	o := owner("orders", "uid-1")

	data := binding.Data{"host": []byte("db"), "password": []byte("p1")}
	if err := binding.WriteSecret(ctx, c, o, "orders-db", binding.TypePostgreSQL, binding.DefaultProvider, data,
		binding.WithManagedBy("operator-db")); err != nil {
		t.Fatal(err)
	}
	s := getSecret(t, c, "orders-db")
	if s.Type != "servicebinding.io/postgresql" {
		t.Errorf("type = %q", s.Type)
	}
	if string(s.Data["type"]) != "postgresql" || string(s.Data["provider"]) != "craftainer.dev" ||
		string(s.Data["host"]) != "db" || string(s.Data["password"]) != "p1" {
		t.Errorf("data = %v", s.Data)
	}
	if s.Labels[binding.LabelManagedBy] != "operator-db" || s.Labels[binding.LabelClaim] != "orders" {
		t.Errorf("labels = %v", s.Labels)
	}
	if ref := metav1.GetControllerOf(s); ref == nil || ref.UID != "uid-1" || ref.Kind != "Gadget" {
		t.Errorf("controller reference = %+v", ref)
	}

	// The caller's map is not retained, and a rewrite replaces the data and
	// keeps foreign labels.
	data["password"][0] = 'X'
	if string(getSecret(t, c, "orders-db").Data["password"]) != "p1" {
		t.Error("WriteSecret kept the caller's slice")
	}
	s.Labels["extra"] = "kept"
	if err := c.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := binding.WriteSecret(ctx, c, o, "orders-db", binding.TypePostgreSQL, binding.DefaultProvider,
		binding.Data{"password": []byte("p2")}); err != nil {
		t.Fatal(err)
	}
	s = getSecret(t, c, "orders-db")
	if string(s.Data["password"]) != "p2" || s.Data["host"] != nil || s.Labels["extra"] != "kept" {
		t.Errorf("after rewrite: data %v labels %v", s.Data, s.Labels)
	}
	if s.Labels[binding.LabelManagedBy] != binding.DefaultManagedBy {
		t.Errorf("managed-by = %q, want the default", s.Labels[binding.LabelManagedBy])
	}
}

func TestWriteSecretRefusesToTakeOver(t *testing.T) {
	ctx := context.Background()
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: "ns"}, Data: map[string][]byte{"k": []byte("v")}}
	yes := true
	otherOwner := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "theirs", Namespace: "ns",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "test.craftainer.dev/v1alpha1", Kind: "Gadget", Name: "other",
			UID: "uid-other", Controller: &yes}}}}
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(foreign, otherOwner).Build()
	o := owner("orders", "uid-1")

	for _, name := range []string{"mine", "theirs"} {
		err := binding.WriteSecret(ctx, c, o, name, binding.TypeS3, binding.DefaultProvider, binding.Data{"k": []byte("x")})
		if !errors.Is(err, binding.ErrNameCollision) {
			t.Errorf("%s: err = %v, want ErrNameCollision", name, err)
		}
	}
	if string(getSecret(t, c, "mine").Data["k"]) != "v" {
		t.Error("the foreign Secret was modified")
	}
}

func TestWriteSecretTypeIsImmutable(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	o := owner("orders", "uid-1")
	if err := binding.WriteSecret(ctx, c, o, "s", binding.TypeS3, binding.DefaultProvider, nil); err != nil {
		t.Fatal(err)
	}
	err := binding.WriteSecret(ctx, c, o, "s", binding.TypeRedis, binding.DefaultProvider, nil)
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Errorf("err = %v, want an immutable-type error", err)
	}
}

func TestWriteSecretOwnerNotInScheme(t *testing.T) {
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).Build()
	if err := binding.WriteSecret(context.Background(), c, owner("o", "u"), "s", binding.TypeS3, "p", nil); err == nil {
		t.Error("expected an error for an owner the scheme does not know")
	}
}

func TestCacheByObjectRestrictsSecrets(t *testing.T) {
	by := binding.CacheByObject("operator-db")
	if len(by) != 1 {
		t.Fatalf("entries = %d", len(by))
	}
	for obj, opts := range by {
		if _, ok := obj.(*corev1.Secret); !ok {
			t.Errorf("object = %T", obj)
		}
		if got := opts.Label.String(); got != "app.kubernetes.io/managed-by=operator-db" {
			t.Errorf("selector = %q", got)
		}
	}
}
