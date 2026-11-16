package cloud

import (
	"testing"

	"github.com/craftainer-labs/lib-operator-go/pkg/testenv"
)

func TestDeepCopy(t *testing.T) {
	testenv.VerifyDeepCopy(t,
		&SecretRef{}, &Credentials{}, &NamespacedCredentials{},
		&AWSBlock{}, &AWSClusterBlock{}, &GCPBlock{}, &GCPClusterBlock{}, &AzureBlock{}, &AzureClusterBlock{},
	)
}
