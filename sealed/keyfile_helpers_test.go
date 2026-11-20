//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package sealed

import "syscall"

// mkfifo makes a FIFO at path, at 0600.
func mkfifo(path string) error { return syscall.Mkfifo(path, 0o600) }
