//go:build unix

package testenv

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockSetup serializes the setup-envtest runs of the test binaries that
// `go test ./...` starts in parallel: two of them unpacking the same release
// into the same directory fail each other. It returns the function that
// releases the lock. If the lock file cannot be opened there is no lock and
// the run goes ahead, as it would without this function: f is then nil, and
// the Fd and Close calls below fail harmlessly on a nil *os.File.
func lockSetup() func() {
	f, _ := os.OpenFile(filepath.Join(os.TempDir(), "lib-operator-go-setup-envtest.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_EX) //nolint:gosec // G115: a file descriptor fits in an int
	// Closing the file releases the lock.
	return func() { _ = f.Close() }
}
