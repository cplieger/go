package filesafe

import (
	"fmt"
	"os"
	"path/filepath"
)

// PrivateDirResult reports the outcome of one EnsurePrivateDir call, and only
// beside a nil error: a non-nil error means no verdict, not "nothing
// happened", since the mkdir may have succeeded before a later step refused
// the result. Treat a failure as fatal, except ErrParentNotSynced: the
// directory was left in place, and a retry finds it and syncs again.
type PrivateDirResult struct {
	// Mode is the mode the filesystem actually stored, read back from the
	// open handle (permission bits plus setuid/setgid/sticky). Always 0700
	// when the error is nil, except on the pre-existing-and-already-private
	// path, where it is what was found.
	Mode os.FileMode
	// Created reports whether THIS call created the directory, one no other
	// writer has ever held a name to, so a widened mode is always repaired.
	Created bool
	// Repaired reports whether this call chmod'ed the directory to 0700: a
	// Created directory whose stored mode was not what was asked (an
	// inheritable ACL widening a fresh 0700 directory), which is always
	// repaired, or, under WithRepairOwnedDir, a pre-existing owned directory
	// that granted group or other access.
	Repaired bool
}

// EnforceMode sets want on the open file or directory f (fchmod), reads the
// mode back from the handle (fstat) and returns what the filesystem stored,
// since the umask narrows a requested mode and an inherited ACL can widen it
// (docs/file-modes.md). A mismatch in the permission, setuid, setgid or sticky
// bits returns ErrModeNotStored with the stored mode. A nil f returns
// ErrUnsafePath; Windows returns ErrModeUnsupported and leaves f unchanged.
func EnforceMode(f *os.File, want os.FileMode) (os.FileMode, error) {
	if f == nil {
		return 0, fmt.Errorf("%w: nil file", ErrUnsafePath)
	}
	if err := modesSupported(); err != nil {
		return 0, fmt.Errorf("%w: EnforceMode", err)
	}
	// (*os.File).Chmod is fchmod(2) on the descriptor, not chmod(2) on the name.
	if err := f.Chmod(want); err != nil {
		return 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	stored, asked := chmodBits(fi.Mode()), chmodBits(want)
	if stored != asked {
		return stored, &ModeNotStoredError{Name: filepath.Clean(f.Name()), Requested: asked, Stored: stored}
	}
	return stored, nil
}

// enforceMode is the write path's EnforceMode. It is a package var so tests
// can stand in for a filesystem that refuses a mode.
var enforceMode = EnforceMode

// chmodBits reduces a mode to the bits chmod(2) can set, dropping the type
// bits so a file and a directory compare.
func chmodBits(m os.FileMode) os.FileMode {
	return m.Perm() | m&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
}

// octalMode renders chmodBits(m) in chmod(1) octal. os.FileMode keeps setuid,
// setgid and sticky above the low 12 bits, so %o alone would print a sticky
// 0770 as 04000770.
func octalMode(m os.FileMode) string {
	o := uint32(m.Perm())
	if m&os.ModeSetuid != 0 {
		o |= 0o4000
	}
	if m&os.ModeSetgid != 0 {
		o |= 0o2000
	}
	if m&os.ModeSticky != 0 {
		o |= 0o1000
	}
	return fmt.Sprintf("%#o", o)
}

// EnsurePrivateDir creates the one absolute directory level dir at 0700 if
// absent, proves from an open handle that it is a directory the effective uid
// owns with no group or other access, then fsyncs dir's parent; the verdict
// holds at one point in time (docs/file-modes.md). Refusals (errors.Is):
// ErrModeTooOpen unless WithRepairOwnedDir, ErrEmptyPath, ErrUnsafePath,
// ErrSymlinkTarget, ErrNotDirectory, ErrNotOwned, ErrModeNotStored,
// fs.ErrPermission for an unreadable parent, and ErrParentNotSynced, the one
// worth a retry. On Windows a valid dir returns ErrModeUnsupported.
func EnsurePrivateDir(dir string, opts ...PrivateDirOption) (PrivateDirResult, error) {
	c := buildPrivateDirCfg(opts)
	clean, err := validateAbsClean(dir)
	if err != nil {
		return PrivateDirResult{}, err
	}
	return ensurePrivateDir(clean, c)
}

// CheckPrivateDir proves from an open handle that the absolute dir is a
// directory, not a symlink, owned by the effective uid with no group or other
// access: EnsurePrivateDir's verdict, reached without creating, changing or
// syncing anything. A missing dir matches fs.ErrNotExist, an unsearchable
// parent fs.ErrPermission; refusals are EnsurePrivateDir's (ErrEmptyPath,
// ErrUnsafePath, ErrSymlinkTarget, ErrNotDirectory, ErrNotOwned,
// ErrModeTooOpen). On Windows a valid dir returns ErrModeUnsupported.
func CheckPrivateDir(dir string) error {
	clean, err := validateAbsClean(dir)
	if err != nil {
		return err
	}
	return checkPrivateDir(clean)
}

// CheckPrivateDirInRoot is CheckPrivateDir for name inside root, where "."
// checks root's own directory as it is now. A symlink at name is refused, an
// in-root symlinked ancestor resolves, and a name whose identity changes
// during the check is ErrRaced. A relative, NUL-free name is required
// (ErrEmptyPath, ErrUnsafePath) and a nil root returns ErrUnsafePath.
func CheckPrivateDirInRoot(root *os.Root, name string) error {
	if root == nil {
		return fmt.Errorf("%w: nil root", ErrUnsafePath)
	}
	clean, err := validateRootName(name)
	if err != nil {
		return err
	}
	return checkPrivateDirInRoot(root, clean)
}
