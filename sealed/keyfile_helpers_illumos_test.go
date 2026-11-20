package sealed

import "syscall"

// mkfifo makes a FIFO at path, at 0600, with mknod(2): package syscall has
// no Mkfifo for illumos.
func mkfifo(path string) error { return syscall.Mknod(path, syscall.S_IFIFO|0o600, 0) }
