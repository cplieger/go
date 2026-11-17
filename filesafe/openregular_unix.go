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
// what is at name with lstat: ErrSymlinkTarget for ELOOP (an ancestor loop
// too) or a link, since FreeBSD and DragonFly refuse a final link with
// EMLINK, NetBSD with EFTYPE and DragonFly also with EINVAL (Go's
// os.isNoFollowErr), and socketRefusal's verdict otherwise.
func noFollowRefusal(err error, name string, lstat func() (os.FileInfo, error)) error {
	if errors.Is(err, syscall.ELOOP) {
		return fmt.Errorf("%w: %s: %w", ErrSymlinkTarget, name, err)
	}
	fi, lerr := lstat()
	if lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s: %w", ErrSymlinkTarget, name, err)
	}
	return socketRefusal(err, name, func() (os.FileInfo, error) { return fi, lerr })
}
