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

// These are the same kind registered in versions whose names sort the wrong
// way as strings: "v1beta10" < "v1beta2", and "v1" < "v1beta10".
type (
	v1beta2Server  struct{ testapi.ClusterWidgetServer }
	v1beta10Server struct{ testapi.ClusterWidgetServer }
	v1Server       struct{ testapi.ClusterWidgetServer }
)

func (s *v1beta2Server) DeepCopyObject() runtime.Object  { c := *s; return &c }
func (s *v1beta10Server) DeepCopyObject() runtime.Object { c := *s; return &c }
func (s *v1Server) DeepCopyObject() runtime.Object       { c := *s; return &c }

func TestNewServerPicksTheLatestVersionNotTheGreatestString(t *testing.T) {
	gvk := func(version string) schema.GroupVersionKind {
		return schema.GroupVersionKind{Group: "same.example", Version: version, Kind: "ClusterWidgetServer"}
	}
	tests := []struct {
		name string
		reg  map[string]runtime.Object
		want runtime.Object
	}{
		{"v1beta10 over v1beta2", map[string]runtime.Object{"v1beta2": &v1beta2Server{}, "v1beta10": &v1beta10Server{}}, &v1beta10Server{}},
		{"v1 over v1beta10", map[string]runtime.Object{"v1beta10": &v1beta10Server{}, "v1": &v1Server{}}, &v1Server{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := runtime.NewScheme()
			for v, obj := range tt.reg {
				s.AddKnownTypeWithName(gvk(v), obj)
			}
			got, err := newServer(s, "ClusterWidgetServer")
			if err != nil {
				t.Fatal(err)
			}
			if reflect.TypeOf(got) != reflect.TypeOf(tt.want) {
				t.Errorf("newServer = %T, want %T", got, tt.want)
			}
		})
	}
}
