//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package envx

import "syscall"

// mkfifoForTest makes a FIFO at path.
func mkfifoForTest(path string) error { return syscall.Mkfifo(path, 0o600) }
