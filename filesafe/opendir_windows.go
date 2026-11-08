package filesafe

import (
	"io/fs"
	"os"
	"syscall"
)

// openDirInRoot opens dir inside root read-only, refusing anything that is
// not a directory with ENOTDIR, the error the Unix open gives. Go exposes no
// O_DIRECTORY on Windows, so the check runs on the opened handle; a plain
// open cannot hang there, because Windows filesystems hold no FIFOs.
func openDirInRoot(root *os.Root, dir string) (*os.File, error) {
	d, err := root.OpenFile(dir, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	fi, err := d.Stat()
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	if !fi.IsDir() {
		_ = d.Close()
		return nil, &fs.PathError{Op: "openat", Path: dir, Err: syscall.ENOTDIR}
	}
	return d, nil
}

// dirOpenName is name unchanged: Windows has no FIFO to wait on.
func dirOpenName(name string) string { return name }
