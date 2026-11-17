//go:build !windows

package filesafe

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openNoFollow opens clean read-only, refusing a final-component symlink with
// ErrSymlinkTarget. O_NONBLOCK turns a FIFO into a rejection rather than a
// hang.
func openNoFollow(clean string) (*os.File, error) {
	f, err := os.OpenFile(clean, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, noFollowRefusal(err, clean, func() (os.FileInfo, error) { return os.Lstat(clean) })
	}
	return f, nil
}

// noFollowRefusal names what an O_NOFOLLOW open of name refused, reading
// what is at name with lstat: ErrSymlinkTarget for a link (an ancestor
// ELOOP takes the same arm), and socketRefusal's verdict otherwise.
func noFollowRefusal(err error, name string, lstat func() (os.FileInfo, error)) error {
	if errors.Is(err, syscall.ELOOP) {
		return fmt.Errorf("%w: %s: %w", ErrSymlinkTarget, name, err)
	}
	return socketRefusal(err, name, lstat)
}
