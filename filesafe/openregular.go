package filesafe

import (
	"fmt"
	"os"
	"syscall"
)

// OpenRegularInRoot opens name inside root read-only and returns the open
// descriptor with that descriptor's FileInfo, refusing anything not a regular
// file with ErrNotRegular naming the mode. An in-root symlink resolves;
// OpenRegularInRootNoFollow refuses one. The caller owns the file and root. A
// relative, NUL-free name is required (ErrEmptyPath, ErrUnsafePath), a nil
// root returns ErrUnsafePath, and other errors are the root's own.
func OpenRegularInRoot(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	if root == nil {
		return nil, nil, fmt.Errorf("%w: nil root", ErrUnsafePath)
	}
	clean, err := validateRootName(name)
	if err != nil {
		return nil, nil, err
	}

	// O_NONBLOCK makes an open on a planted FIFO a rejection rather than a hang.
	f, err := root.OpenFile(clean, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	fi, err := statRegular(f, clean)
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

// OpenRegularNoFollow opens the absolute path read-only and returns the open
// descriptor with that descriptor's FileInfo, refusing anything not a
// regular file with ErrNotRegular naming the mode, and a symlink at the final
// component with ErrSymlinkTarget: the NoFollow suffix means the same as on
// OpenRegularInRootNoFollow. Ancestor components are not pinned, and the open
// is non-blocking so a FIFO cannot stall the caller. An empty, NUL-holding or
// relative path is ErrEmptyPath or ErrUnsafePath; other errors are the
// filesystem's own (a missing file matches fs.ErrNotExist). On Windows the
// open handle does not block a filesafe write of the file.
func OpenRegularNoFollow(path string) (*os.File, os.FileInfo, error) {
	clean, err := validateAbsClean(path)
	if err != nil {
		return nil, nil, err
	}

	f, err := openNoFollow(clean)
	if err != nil {
		return nil, nil, err
	}
	fi, err := statRegular(f, clean)
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

// statRegular stats the open descriptor, never the path, so the verdict is
// about the file actually opened.
func statRegular(f *os.File, name string) (os.FileInfo, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, notRegular(name, fi.Mode())
	}
	return fi, nil
}

// OpenRegularInRootNoFollow is OpenRegularInRoot that refuses a symlink at the
// final component with ErrSymlinkTarget instead of resolving it. A name whose
// identity changes between the check and the open is ErrRaced. It is a
// separate function so that no zero-value option selects the permissive
// policy. Ancestor components are not pinned (see OpenParentInRoot).
func OpenRegularInRootNoFollow(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	if root == nil {
		return nil, nil, fmt.Errorf("%w: nil root", ErrUnsafePath)
	}
	clean, err := validateRootName(name)
	if err != nil {
		return nil, nil, err
	}

	// os.Root resolves in-root symlinks before opening the final component, so
	// O_NOFOLLOW is inert through Root.OpenFile; Root.Lstat is the refusal, and
	// the descriptor's identity is checked against it below.
	before, err := root.Lstat(clean)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("%w: %s", ErrSymlinkTarget, clean)
	}

	// O_NONBLOCK for the same reason as the sibling: a FIFO that slipped in
	// between the Lstat and here is a rejection rather than a hang.
	f, err := root.OpenFile(clean, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	// Identity BEFORE kind: a name swapped for a directory is more honestly
	// reported as a race than as "not a regular file", because the caller's
	// question was about the object it checked.
	if !os.SameFile(before, fi) {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%w: %s", ErrRaced, clean)
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, notRegular(clean, fi.Mode())
	}
	return f, fi, nil
}
