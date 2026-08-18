//go:build !unix

package lock

import "os"

// flock is a no-op on unsupported platforms (v0 only supports Linux/macOS).
func flock(f *os.File, release bool) error { return nil }
