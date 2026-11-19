//go:build !windows

package filesafe

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// privateDirMode is the mode EnsurePrivateDir requests and enforces:
// owner-only access.
const privateDirMode os.FileMode = 0o700

// privateDirCreatedHook, when set by a test, runs after ensurePrivateDir
// created the directory and before it opens it, the point where a
// filesystem that ignores the requested mode has already stored its own.
var privateDirCreatedHook func(dir string)

// fsyncDir opens dir as a directory and fsyncs it. It is a package var so
// tests can record the sync and inject a failure.
var fsyncDir = func(dir string) error {
	d, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// ensurePrivateDir is EnsurePrivateDir after path validation; the sequence
// and every refusal are documented there.
func ensurePrivateDir(clean string, c *cfg) (PrivateDirResult, error) {
	created, err := mkdirPrivate(clean)
	if err != nil {
		return PrivateDirResult{}, err
	}
	if created && privateDirCreatedHook != nil {
		privateDirCreatedHook(clean)
	}
	f, err := openPrivateDir(clean)
	if err != nil {
		return PrivateDirResult{}, err
	}
	defer func() { _ = f.Close() }()

	fi, err := statOwnedDir(f, clean)
	if err != nil {
		return PrivateDirResult{}, err
	}
	var res PrivateDirResult
	if created {
		res, err = enforceCreatedDir(f, fi, clean, c)
	} else {
		res, err = verifyFoundDir(f, fi, clean, c)
	}
	if err != nil {
		return PrivateDirResult{}, err
	}
	if syncErr := syncParent(clean, created); syncErr != nil {
		return PrivateDirResult{}, syncErr
	}
	return res, nil
}

// enforceCreatedDir sets 0700 on a directory this call created, repairing a
// mode the filesystem widened.
func enforceCreatedDir(f *os.File, fi os.FileInfo, clean string, c *cfg) (PrivateDirResult, error) {
	stored, err := EnforceMode(f, privateDirMode)
	if err != nil {
		return PrivateDirResult{}, err
	}
	found := chmodBits(fi.Mode())
	repaired := found != privateDirMode
	if repaired {
		c.logger.Warn("filesafe: created directory did not keep the requested mode; repaired",
			"dir", clean, "requested", privateDirMode.String(), "stored", found.String())
	}
	return PrivateDirResult{Mode: stored, Created: true, Repaired: repaired}, nil
}

// verifyFoundDir is the verdict on an owned directory this call did not
// create: private as found, or repaired under WithRepairOwnedDir.
func verifyFoundDir(f *os.File, fi os.FileInfo, clean string, c *cfg) (PrivateDirResult, error) {
	found := chmodBits(fi.Mode())
	modeErr := privateModeErr(fi, clean)
	if modeErr == nil {
		return PrivateDirResult{Mode: found}, nil
	}
	// statOwnedDir already proved this directory is ours, which is what makes
	// narrowing it our call rather than an intrusion.
	if !c.repairOwnedDir {
		return PrivateDirResult{}, fmt.Errorf(
			"%w; a pre-existing directory is never repaired without WithRepairOwnedDir(true)", modeErr,
		)
	}
	stored, err := EnforceMode(f, privateDirMode)
	if err != nil {
		return PrivateDirResult{}, err
	}
	c.logger.Warn("filesafe: pre-existing directory was not private; repaired",
		"dir", clean, "requested", privateDirMode.String(), "found", found.String())
	return PrivateDirResult{Mode: stored, Repaired: true}, nil
}

// checkPrivateDir is CheckPrivateDir after path validation.
func checkPrivateDir(clean string) error {
	f, err := openPrivateDir(clean)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = checkPrivateDirHandle(f, clean)
	return err
}

// checkPrivateDirInRoot is CheckPrivateDirInRoot after argument validation.
// os.Root follows an in-root symlink at the final component, so the Lstat
// before the open is the refusal, and SameFile proves the open reached the
// directory the Lstat saw.
func checkPrivateDirInRoot(root *os.Root, clean string) error {
	before, err := root.Lstat(clean)
	switch {
	case err != nil:
		return err
	case before.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%w: %s", ErrSymlinkTarget, clean)
	case !before.IsDir():
		return fmt.Errorf("%w: %s (type %s)", ErrNotDirectory, clean, before.Mode().Type())
	}
	if pinOpenHook != nil {
		pinOpenHook(clean)
	}
	f, err := openDirInRoot(root, clean)
	if err != nil {
		return racedOpen(root, clean, before, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := checkPrivateDirHandle(f, clean)
	if err != nil {
		return err
	}
	if !os.SameFile(before, fi) {
		return fmt.Errorf("%w: %s", ErrRaced, clean)
	}
	return nil
}

// checkPrivateDirHandle is the private-directory verdict on an open
// directory; ensurePrivateDir reaches the same one through its two halves.
func checkPrivateDirHandle(f *os.File, dir string) (os.FileInfo, error) {
	fi, err := statOwnedDir(f, dir)
	if err != nil {
		return nil, err
	}
	return fi, privateModeErr(fi, dir)
}

// privateModeErr is ErrModeTooOpen when fi grants group or other access.
func privateModeErr(fi os.FileInfo, dir string) error {
	if m := chmodBits(fi.Mode()); m&0o077 != 0 {
		return fmt.Errorf("%w: %s (mode %#o)", ErrModeTooOpen, dir, m)
	}
	return nil
}

// syncParent fsyncs dir's parent. On failure it removes a dir this call
// created with rmdir(2), which removes only an empty directory and never a
// file or link swapped in at the name.
func syncParent(dir string, created bool) error {
	parent := filepath.Dir(dir)
	err := fsyncDir(parent)
	if err == nil {
		return nil
	}
	if !created {
		return fmt.Errorf("%w: %s (%s found; left in place): %w", ErrParentNotSynced, parent, dir, err)
	}
	if rmErr := syscall.Rmdir(dir); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s (%s created; not removed): %w", ErrParentNotSynced, parent, dir,
			errors.Join(err, fmt.Errorf("filesafe: removing %s: %w", dir, rmErr)))
	}
	return fmt.Errorf("%w: %s (%s created; removed again): %w", ErrParentNotSynced, parent, dir, err)
}

// mkdirPrivate creates dir at 0700 and reports whether THIS call created
// it. fs.ErrExist means it did not, not a failure; every other error is the
// filesystem's.
func mkdirPrivate(dir string) (bool, error) {
	err := os.Mkdir(dir, privateDirMode)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	return false, err
}

// openPrivateDir opens dir with O_DIRECTORY|O_NOFOLLOW, so the kernel
// refuses a non-directory or a final-component link before this process
// holds a descriptor, and O_NONBLOCK, so a planted FIFO cannot stall the
// open. Which errno names that refusal differs by kernel, hence
// occupantRefusal.
func openPrivateDir(dir string) (*os.File, error) {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, occupantRefusal(err, dir, func() (os.FileInfo, error) { return os.Lstat(dir) })
	}
	return f, nil
}

// occupantRefusal names a refused open by what lstat finds at dir, since
// each kernel picks its own errno (a link is ENOTDIR on Linux, EMLINK on
// FreeBSD; a socket is EOPNOTSUPP on OpenBSD): ErrSymlinkTarget for a link,
// ErrNotDirectory for another non-directory, the open's error for a
// directory or a failed lstat. A name swapped in after the failed open can
// only mislabel the error, never admit a descriptor.
func occupantRefusal(err error, dir string, lstat func() (os.FileInfo, error)) error {
	fi, lerr := lstat()
	switch {
	case lerr != nil || fi.IsDir():
		return err
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%w: %s: %w", ErrSymlinkTarget, dir, err)
	default:
		return fmt.Errorf("%w: %s: %w", ErrNotDirectory, dir, err)
	}
}

// statOwnedDir stats the OPEN descriptor and refuses anything that is not
// a directory owned by the effective uid — the handle, not the path, so no
// later check depends on the name. The type check is not redundant with
// O_DIRECTORY: it stays true even if the open flags ever change.
func statOwnedDir(f *os.File, dir string) (os.FileInfo, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsDir() {
		return nil, fmt.Errorf("%w: %s (type %s)", ErrNotDirectory, dir, fi.Mode().Type())
	}
	if err := ownerErr(fi, dir); err != nil {
		return nil, err
	}
	return fi, nil
}

// ownerErr is ErrNotOwned unless fi is owned by the effective uid.
func ownerErr(fi os.FileInfo, name string) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: %s: ownership could not be determined", ErrNotOwned, name)
	}
	if euid := os.Geteuid(); int(st.Uid) != euid {
		return fmt.Errorf("%w: %s: owned by uid %d, want the effective uid %d; its owner could replace the checked path",
			ErrNotOwned, name, st.Uid, euid)
	}
	return nil
}
