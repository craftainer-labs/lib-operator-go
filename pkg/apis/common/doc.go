// Package common holds the Go types every craftainer operator embeds in its
// CRDs (ADR 0002, sections 1 and 3): the claim spec and status blocks, the
// rotation policy, the server blocks and allowedNamespaces. The kubebuilder
// markers on them are the schema and CEL rules of docs/crd-drafts.md, so an
// operator's controller-gen run produces them without copying anything.
//
// The deep-copy methods in zz_generated.deepcopy.go are generated:
// run `go generate ./pkg/apis/common/...` after changing a type.
//
// +kubebuilder:object:generate=true
package common

//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.22.0 object paths=.
