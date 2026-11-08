//go:build !windows

package filesafe

import (
	"os"
	"path/filepath"
	"syscall"
)

// openDirInRoot opens dir inside root read-only, refusing anything that is
// not a directory with ENOTDIR. O_DIRECTORY, not root.Open: a reader-less
// FIFO would block a plain O_RDONLY open(2) forever. O_NONBLOCK covers the
// same hazard again.
func openDirInRoot(root *os.Root, dir string) (*os.File, error) {
	return root.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
}

// dirOpenName is name with "/." appended, so opening it resolves name as an
// intermediate component, which the kernel requires to be a directory: a
// FIFO, socket or file there answers ENOTDIR at once instead of an open that
// waits on a FIFO with no writer. os.OpenRoot and os.Open open their final
// component with neither O_DIRECTORY nor O_NONBLOCK.
func dirOpenName(name string) string { return name + string(filepath.Separator) + "." }
