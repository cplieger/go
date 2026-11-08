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

// ensurePrivateDir is EnsurePrivateDir after path validation; the sequence
// and every refusal are documented there.
func ensurePrivateDir(clean string, c *cfg) (PrivateDirResult, error) {
	created, err := mkdirPrivate(clean)
	if err != nil {
		return PrivateDirResult{}, err
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
// open. With O_DIRECTORY set, Linux reports a final-component symlink as
// ENOTDIR rather than ELOOP, hence refusedOccupant.
func openPrivateDir(dir string) (*os.File, error) {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err == nil {
		return f, nil
	}
	if errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ELOOP) {
		return nil, refusedOccupant(dir, err)
	}
	return nil, err
}

// refusedOccupant names what the kernel refused: ErrSymlinkTarget for a
// link, ErrNotDirectory for anything else. Its second look at the pathname
// is sound because the open already failed: a name swapped in between can
// only mislabel the error, never admit a descriptor.
func refusedOccupant(dir string, err error) error {
	if fi, lerr := os.Lstat(dir); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s: %w", ErrSymlinkTarget, dir, err)
	}
	return fmt.Errorf("%w: %s: %w", ErrNotDirectory, dir, err)
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
