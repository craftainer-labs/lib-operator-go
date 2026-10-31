module github.com/craftainer/template-go-module

// The minimum Go release the module supports: consumers need at least this.
// Bump it only when the code needs a newer language feature, never
// automatically. The toolchain line is what the repository builds with and
// moves with scripts/develop.d/10-go.sh's GO_VERSION.
go 1.26.0

toolchain go1.27.3
