package filesafe

import (
	"path/filepath"
	"syscall"
)

// MkfifoForTest creates a FIFO at path with mknodat(2): package syscall has
// no Mkfifo or Mknod for aix, and keeps AT_FDCWD unexported, so the path is
// made absolute, which POSIX mknodat resolves without the directory fd.
func MkfifoForTest(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	return syscall.Mknodat(-1, abs, syscall.S_IFIFO|0o600, 0)
}
