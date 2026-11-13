//go:build !unix

package testenv

// lockSetup is a no-op where there is no flock.
func lockSetup() func() { return func() {} }
