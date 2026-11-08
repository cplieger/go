//go:build !windows

package filesafe

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// openDirInRoot opens dir inside root read-only, refusing anything that is
// not a directory with ENOTDIR. O_DIRECTORY, not root.Open: a reader-less
// FIFO would block a plain O_RDONLY open(2) forever. O_NONBLOCK covers the
// same hazard again.
func openDirInRoot(root *os.Root, dir string) (*os.File, error) {
	return root.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
}

// dirOpenName is an ambient path with a trailing separator, which the kernel
// resolves only to a directory, in the same path walk as the open: a FIFO,
// socket or file there answers ENOTDIR at once instead of an open that waits
// on a FIFO with no writer. os.OpenRoot and os.Open open their final component
// with neither O_DIRECTORY nor O_NONBLOCK. A root opened this way names its
// children cleanly ("dir/" joined with "x" is "dir/x"), so only an open's own
// error carries the separator, which dirOpenErr takes back out.
//
// It is for os.Open and os.OpenRoot only. Inside an os.Root, a trailing
// separator is resolved in user space, by an lstat and then an openat without
// O_DIRECTORY, so a FIFO swapped in between the two would hang the open; an
// in-root open uses childDirOpenName.
func dirOpenName(name string) string {
	if strings.HasSuffix(name, string(filepath.Separator)) {
		return name
	}
	return name + string(filepath.Separator)
}

// childDirOpenName is an in-root name with "/." appended, for Root.OpenRoot.
// os.Root opens every component but the last with O_DIRECTORY, so name is
// opened as a directory and "." inside it: a FIFO swapped in for name answers
// ENOTDIR instead of blocking. The child root keeps the "/." in its Name()
// ("<root>/d/."); every path this package reports goes through filepath.Join
// or filepath.Clean, or is rewritten by dirOpenErr.
func childDirOpenName(name string) string { return name + string(filepath.Separator) + "." }
