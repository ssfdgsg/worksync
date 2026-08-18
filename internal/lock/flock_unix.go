//go:build unix

package lock

import (
	"os"
	"syscall"
)

// flock takes (or releases) an exclusive advisory lock on f.
func flock(f *os.File, release bool) error {
	op := syscall.LOCK_EX | syscall.LOCK_NB
	if release {
		op = syscall.LOCK_UN
	}
	return syscall.Flock(int(f.Fd()), op)
}
