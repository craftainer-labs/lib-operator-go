package testapi

import (
	"embed"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/yaml"
)

//go:embed crds/*.yaml
var crdFiles embed.FS

// CRDs returns the generated CRDs of the test kinds, for testenv.Options.CRDs.
// The files are embedded, so a read or parse failure is a build defect and
// panics.
func CRDs() []*apiextensionsv1.CustomResourceDefinition {
	entries, err := crdFiles.ReadDir("crds")
	utilruntime.Must(err)
	var out []*apiextensionsv1.CustomResourceDefinition
	for _, e := range entries {
		raw, err := crdFiles.ReadFile("crds/" + e.Name())
		utilruntime.Must(err)
		var crd apiextensionsv1.CustomResourceDefinition
		utilruntime.Must(yaml.UnmarshalStrict(raw, &crd))
		out = append(out, &crd)
	}
	return out
}
