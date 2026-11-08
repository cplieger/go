//go:build !windows

package filesafe

import (
	"os"
	"syscall"
)

// openRead opens clean read-only, following symlinks. O_NONBLOCK makes an
// open on a FIFO with no writer return at once, so the caller's regular-file
// check refuses it rather than the open hanging; it changes nothing for a
// regular file's reads.
func openRead(clean string) (*os.File, error) {
	f, err := os.OpenFile(clean, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, socketRefusal(err, clean, func() (os.FileInfo, error) { return os.Stat(clean) })
	}
	return f, nil
}
