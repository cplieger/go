//go:build !windows

package filesafe

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openNoFollow opens clean read-only, refusing a final-component symlink with
// ErrSymlinkTarget (an ancestor ELOOP takes the same arm). O_NONBLOCK turns a
// FIFO into a rejection rather than a hang.
func openNoFollow(clean string) (*os.File, error) {
	f, err := os.OpenFile(clean, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: %s: %w", ErrSymlinkTarget, clean, err)
		}
		return nil, socketRefusal(err, clean, func() (os.FileInfo, error) { return os.Lstat(clean) })
	}
	return f, nil
}
