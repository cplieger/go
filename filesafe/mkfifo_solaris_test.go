package filesafe

import "syscall"

// MkfifoForTest creates a FIFO at path with mknod(2): package syscall has no
// Mkfifo for solaris or illumos.
func MkfifoForTest(path string) error { return syscall.Mknod(path, syscall.S_IFIFO|0o600, 0) }
