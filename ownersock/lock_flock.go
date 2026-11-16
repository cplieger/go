//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package ownersock

import (
	"fmt"
	"os"
	"syscall"
)

// lockDir takes an exclusive flock(2) on dir and returns its release. Two
// opens of one directory hold separate locks, so starts in one process
// exclude each other as starts in two do.
func lockDir(dir string) (unlock func(), err error) {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("ownersock: opening the socket directory to lock it: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("ownersock: locking the socket directory: %w", err)
	}
	return func() { _ = f.Close() }, nil
}
