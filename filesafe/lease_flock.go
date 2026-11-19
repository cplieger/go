//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package filesafe

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// lockFileMode is the mode a created lock file gets, so no other user can
// open it and hold the lease against its owner.
const lockFileMode os.FileMode = 0o600

// takeLease is one attempt at the lease. The Lstat after the flock proves
// the locked file is still the one at clean.
func takeLease(clean string, shared bool) (*Lease, error) {
	f, created, err := openLockFile(clean, shared)
	if err != nil {
		return nil, err
	}
	fi, err := checkLockFile(f, clean, created)
	if err == nil {
		err = flockNonBlocking(f, clean, shared)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if leaseLockedHook != nil {
		leaseLockedHook(clean)
	}
	at, err := os.Lstat(clean)
	switch {
	case errors.Is(err, fs.ErrNotExist) || err == nil && !os.SameFile(fi, at):
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s: the lock file was replaced", ErrRaced, clean)
	case err != nil:
		_ = f.Close()
		return nil, err
	}
	return &Lease{f: f, path: clean}, nil
}

// openLockFile opens clean without following a final symlink, O_NONBLOCK so
// a FIFO cannot stall it. The exclusive form opens for writing, which NFS
// and SMB need to emulate flock with a write lock (flock(2) "NFS details",
// "CIFS details"), and creates the file when absent; the shared form opens
// read-only and never creates. A name removed between the exclusive form's
// two opens is ErrRaced.
func openLockFile(clean string, shared bool) (f *os.File, created bool, err error) {
	if shared {
		f, err = os.OpenFile(clean, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, false, lockOpenRefusal(err, clean)
		}
		return f, false, nil
	}
	f, err = os.OpenFile(clean, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, lockFileMode)
	if err == nil {
		return f, true, nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return nil, false, lockOpenRefusal(err, clean)
	}
	f, err = os.OpenFile(clean, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, fmt.Errorf("%w: %s: %w", ErrRaced, clean, err)
	case err != nil:
		return nil, false, lockOpenRefusal(err, clean)
	}
	return f, false, nil
}

// lockOpenRefusal names a refused lock-file open by what lstat finds at
// name, since kernels pick different errnos (ELOOP, EMLINK or EFTYPE for a
// link, EISDIR for a directory, EACCES for another user's file). A name
// swapped after the failed open can only mislabel the error.
func lockOpenRefusal(err error, name string) error {
	if errors.Is(err, syscall.ELOOP) {
		return fmt.Errorf("%w: %s: %w", ErrSymlinkTarget, name, err)
	}
	fi, lerr := os.Lstat(name)
	switch {
	case lerr != nil:
		return err
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%w: %s: %w", ErrSymlinkTarget, name, err)
	case !fi.Mode().IsRegular():
		return notRegular(name, fi.Mode())
	}
	if oerr := ownerErr(fi, name); oerr != nil {
		return fmt.Errorf("%w: %w", oerr, err)
	}
	return err
}

// checkLockFile refuses, from the open descriptor, a lock file that is not
// a regular file owned by the effective uid, or one found with group or
// other access. A created file is set to lockFileMode, since the umask may
// have narrowed it; a failure leaves the file, as every lock file stays.
func checkLockFile(f *os.File, clean string, created bool) (os.FileInfo, error) {
	fi, err := statRegular(f, clean)
	if err != nil {
		return nil, err
	}
	if err := ownerErr(fi, clean); err != nil {
		return nil, err
	}
	if created {
		if _, err := enforceMode(f, lockFileMode); err != nil {
			return nil, err
		}
		return fi, nil
	}
	return fi, privateModeErr(fi, clean)
}

// flockNonBlocking takes flock(2) with LOCK_NB, retrying EINTR. A lock held
// elsewhere is errLeaseBusy.
func flockNonBlocking(f *os.File, clean string, shared bool) error {
	how := syscall.LOCK_EX
	if shared {
		how = syscall.LOCK_SH
	}
	for {
		err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return errLeaseBusy
		default:
			return &fs.PathError{Op: "flock", Path: clean, Err: err}
		}
	}
}
