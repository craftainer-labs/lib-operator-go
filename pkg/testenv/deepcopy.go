package testenv

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/randfill"
)

// VerifyDeepCopy checks the generated deep-copy methods of API types. Each
// object must be a pointer to a struct. It fills the struct with random data
// (no nil pointers, slices or maps), then calls DeepCopy, DeepCopyInto and, if
// the type has it, DeepCopyObject, and fails the test unless each result equals
// the original. It also calls DeepCopy and DeepCopyObject on a nil receiver,
// which must return nil. The same call drives the generated code to full
// statement coverage, which the library's 100% gate needs.
func VerifyDeepCopy(t testing.TB, objs ...any) {
	t.Helper()
	filler := randfill.New().NilChance(0).NumElements(1, 3)
	for _, obj := range objs {
		v := reflect.ValueOf(obj)
		name := v.Type().String()
		filler.Fill(obj)

		if got := v.MethodByName("DeepCopy").Call(nil)[0].Interface(); !equality.Semantic.DeepEqual(obj, got) {
			t.Errorf("%s.DeepCopy() differs from the original", name)
		}
		into := reflect.New(v.Type().Elem())
		v.MethodByName("DeepCopyInto").Call([]reflect.Value{into})
		if !equality.Semantic.DeepEqual(obj, into.Interface()) {
			t.Errorf("%s.DeepCopyInto() differs from the original", name)
		}
		if got := reflect.Zero(v.Type()).MethodByName("DeepCopy").Call(nil)[0]; !got.IsNil() {
			t.Errorf("%s.DeepCopy() of a nil receiver is not nil", name)
		}
		if m := v.MethodByName("DeepCopyObject"); m.IsValid() {
			if got := m.Call(nil)[0].Interface(); !equality.Semantic.DeepEqual(obj, got) {
				t.Errorf("%s.DeepCopyObject() differs from the original", name)
			}
			if got := reflect.Zero(v.Type()).MethodByName("DeepCopyObject").Call(nil)[0]; !got.IsNil() {
				t.Errorf("%s.DeepCopyObject() of a nil receiver is not nil", name)
			}
		}
	}
}
