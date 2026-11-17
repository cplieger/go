//go:build !windows && !solaris && !aix

package filesafe

import "syscall"

// MkfifoForTest creates a FIFO at path. It is exported for the external test
// package; on Windows it returns an error, so callers skip the case.
func MkfifoForTest(path string) error { return syscall.Mkfifo(path, 0o600) }
