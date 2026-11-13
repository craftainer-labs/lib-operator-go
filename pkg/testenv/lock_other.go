//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package testenv

// lockSetup is a no-op where there is no flock.
func lockSetup() func() { return func() {} }
