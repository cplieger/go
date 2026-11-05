//go:build !windows

package filesafe

import (
	"os"
	"syscall"
)

// openDirInRoot opens dir inside root read-only, refusing anything that is
// not a directory with ENOTDIR. O_DIRECTORY, not root.Open: a reader-less
// FIFO would block a plain O_RDONLY open(2) forever. O_NONBLOCK covers the
// same hazard again.
func openDirInRoot(root *os.Root, dir string) (*os.File, error) {
	return root.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
}
