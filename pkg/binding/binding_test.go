// Tests for the binding package's implemented helpers.
package binding_test

import (
	"testing"

	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
)

func TestSecretType(t *testing.T) {
	if got, want := string(binding.SecretType("postgresql")), "servicebinding.io/postgresql"; got != want {
		t.Fatalf("SecretType = %q, want %q", got, want)
	}
}
