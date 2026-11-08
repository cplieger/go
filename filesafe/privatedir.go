package filesafe

import (
	"fmt"
	"os"
	"path/filepath"
)

// PrivateDirResult reports the outcome of one EnsurePrivateDir call.
//
// Only meaningful alongside a nil error: a non-nil error means no verdict
// was reached, which is NOT "nothing happened" — the mkdir may have
// succeeded and a later step refused the result, so the directory can exist
// while its custody is unestablished. Treat a failure as fatal; do not
// retry into it.
type PrivateDirResult struct {
	// Mode is the mode the filesystem actually stored, read back from the
	// open handle (permission bits plus setuid/setgid/sticky). Always 0700
	// when the error is nil, except on the pre-existing-and-already-private
	// path, where it is what was found.
	Mode os.FileMode
	// Created reports whether THIS call created the directory — the field
	// the repair decision turns on: a directory this process just made is
	// one no other writer has ever held a name to.
	Created bool
	// Repaired reports whether this call chmod'ed the directory to 0700: a
	// Created directory whose stored mode was not what was asked (an
	// inheritable ACL widening a fresh 0700 directory), which is always
	// repaired, or, under WithRepairOwnedDir, a pre-existing owned directory
	// that granted group or other access.
	Repaired bool
}

// EnforceMode sets want on the open file or directory f (fchmod), reads the
// mode back from the same handle (fstat) and returns what the filesystem
// stored: a mode given to mkdir or open is only a request, which the umask
// narrows and an inheritable ACL can widen (docs/file-modes.md). Only the
// permission, setuid, setgid and sticky bits are compared. A mismatch returns
// ErrModeNotStored with the stored mode; other errors are the filesystem's.
// A nil f returns ErrUnsafePath. On Windows it returns ErrModeUnsupported and
// leaves f unchanged.
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
		return stored, fmt.Errorf("%w: %s: asked for %#o, filesystem stored %#o",
			ErrModeNotStored, filepath.Clean(f.Name()), asked, stored)
	}
	return stored, nil
}

// enforceMode is the write path's EnforceMode. It is a package var so tests
// can stand in for a filesystem that refuses a mode.
var enforceMode = EnforceMode

// chmodBits reduces a mode to the bits chmod(2) can set: permission bits
// plus setuid, setgid and sticky. Strips the type bits so a file and a
// directory compare meaningfully.
func chmodBits(m os.FileMode) os.FileMode {
	return m.Perm() | m&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
}

// EnsurePrivateDir creates the one absolute directory level dir at 0700 if
// absent, then proves from an open handle that it is a directory owned by
// the effective uid with no group or other access; dir's parent is not
// checked, and the verdict holds at one point in time (docs/file-modes.md).
// A pre-existing too-open directory is ErrModeTooOpen unless
// WithRepairOwnedDir; other refusals are ErrEmptyPath, ErrUnsafePath,
// ErrSymlinkTarget, ErrNotDirectory, ErrNotOwned and ErrModeNotStored (match
// with errors.Is). On Windows a valid dir returns ErrModeUnsupported.
func EnsurePrivateDir(dir string, opts ...PrivateDirOption) (PrivateDirResult, error) {
	c := buildPrivateDirCfg(opts)
	clean, err := validateAbsClean(dir)
	if err != nil {
		return PrivateDirResult{}, err
	}
	return ensurePrivateDir(clean, c)
}
