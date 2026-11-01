package server

import (
	"errors"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/craftainer-labs/lib-operator-go/pkg/internal/testapi"
)

// newerServer is the same kind in a group that sorts after the test group.
type newerServer struct{ testapi.ClusterWidgetServer }

func (s *newerServer) DeepCopyObject() runtime.Object { c := *s; return &c }

func TestNewServerPicksTheGreatestGroupVersion(t *testing.T) {
	s := runtime.NewScheme()
	if err := testapi.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	s.AddKnownTypeWithName(schema.GroupVersionKind{Group: "zz.example", Version: "v1", Kind: "ClusterWidgetServer"}, &newerServer{})

	got, err := newServer(s, "ClusterWidgetServer")
	if err != nil {
		t.Fatal(err)
	}
	if reflect.TypeOf(got) != reflect.TypeOf(&newerServer{}) {
		t.Errorf("newServer = %T, want *newerServer", got)
	}
}

func TestNewServerUnknownKind(t *testing.T) {
	if _, err := newServer(runtime.NewScheme(), "Nope"); !errors.Is(err, ErrUnknownServerKind) {
		t.Errorf("err = %v", err)
	}
}
