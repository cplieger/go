//go:build !windows

package filesafe

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// privateDirMode is the mode EnsurePrivateDir requests and enforces:
// owner-only access.
const privateDirMode os.FileMode = 0o700

// privateDirCreatedHook, when set by a test, runs after ensurePrivateDir
// created the directory and before it opens it, the point where a
// filesystem that ignores the requested mode has already stored its own.
var privateDirCreatedHook func(dir string)

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
	found := chmodBits(fi.Mode())
	if !created {
		if found&0o077 == 0 {
			return PrivateDirResult{Mode: found}, nil
		}
		// The euid-ownership check above already proved this directory is
		// ours, which is what makes narrowing it our call rather than an
		// intrusion.
		if !c.repairOwnedDir {
			return PrivateDirResult{}, fmt.Errorf(
				"%w: %s: mode %#o grants group or other access, and a pre-existing directory is never repaired without WithRepairOwnedDir(true)",
				ErrModeTooOpen, clean, found,
			)
		}
		stored, repairErr := EnforceMode(f, privateDirMode)
		if repairErr != nil {
			return PrivateDirResult{}, repairErr
		}
		c.logger.Warn("filesafe: pre-existing directory was not private; repaired",
			"dir", clean, "requested", privateDirMode.String(), "found", found.String())
		return PrivateDirResult{Mode: stored, Repaired: true}, nil
	}

	stored, err := EnforceMode(f, privateDirMode)
	if err != nil {
		return PrivateDirResult{}, err
	}
	repaired := found != privateDirMode
	if repaired {
		c.logger.Warn("filesafe: created directory did not keep the requested mode; repaired",
			"dir", clean, "requested", privateDirMode.String(), "stored", found.String())
	}
	return PrivateDirResult{Mode: stored, Created: true, Repaired: repaired}, nil
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
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("%w: %s: ownership could not be determined", ErrNotOwned, dir)
	}
	if euid := os.Geteuid(); int(st.Uid) != euid {
		return nil, fmt.Errorf("%w: %s: owned by uid %d, want the effective uid %d; its owner could replace the checked path",
			ErrNotOwned, dir, st.Uid, euid)
	}
	return fi, nil
}
