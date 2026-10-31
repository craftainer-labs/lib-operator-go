// Package greet is the template's sample package: the smallest code that
// exercises every gate (gofmt, golangci-lint, the race detector, the
// coverage gate, govulncheck). Replace it with the module's real packages.
package greet

import "fmt"

// Hello returns a greeting for name, or for "world" when name is empty.
func Hello(name string) string {
	if name == "" {
		name = "world"
	}
	return fmt.Sprintf("Hello, %s!", name)
}
