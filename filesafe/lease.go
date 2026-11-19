package filesafe

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// leasePollInterval bounds how late a waiting AcquireLease sees a released
// lease.
const leasePollInterval = 50 * time.Millisecond

// errLeaseBusy is one attempt's answer while another holder has the lease.
var errLeaseBusy = errors.New("filesafe: lease busy")

// leaseLockedHook, when set by a test, runs after a lease's flock succeeds
// and before the check that the locked file is still the one at its path.
var leaseLockedHook func(path string)

// Lease is a flock(2) on a lock file, held until Close: exclusive from
// AcquireLease and TryLease, shared from AcquireSharedLease and
// TrySharedLease. flock belongs to the open file, so a second lease on one
// path in one process waits as another process's would. A lease holds no
// data: keep yours in a file beside the lock file, written with WriteFile
// while the lease is held. docs/leases.md covers NFS, SMB and hosts sharing
// a mount. On Windows each form fails with ErrModeUnsupported and
// errors.ErrUnsupported, and where syscall has no Flock with the latter.
type Lease struct {
	f    *os.File
	path string
}

// AcquireLease takes an exclusive lease on the lock file at the absolute
// path, creating it at 0600 when absent (never a directory, and never
// removed), and retries every 50 ms until it is free or ctx is done. A done
// ctx opens nothing and returns ctx.Err(); ctx ending while another holder
// has the lease returns ErrLeaseHeld wrapping ctx.Err(). It refuses, before
// locking: a bad path, a symlink (ErrSymlinkTarget), a non-regular file
// (ErrNotRegular), another uid's file (ErrNotOwned), group or other access
// (ErrModeTooOpen), a created file whose 0600 was not stored (ErrModeNotStored).
func AcquireLease(ctx context.Context, path string) (*Lease, error) {
	return acquireLease(ctx, path, false)
}

// TryLease is AcquireLease's single attempt: ErrLeaseHeld at once while
// another holder has the lease, and ErrRaced when the lock file was replaced
// between its open and its lock, which a retry resolves.
func TryLease(path string) (*Lease, error) {
	return tryLease(path, false)
}

// AcquireSharedLease takes a shared lease on the existing lock file at the
// absolute path, held by any number of shared holders together while no
// exclusive holder has it. It never creates the file: a missing one is an
// error matching fs.ErrNotExist. Otherwise it waits, refuses and fails as
// AcquireLease does.
func AcquireSharedLease(ctx context.Context, path string) (*Lease, error) {
	return acquireLease(ctx, path, true)
}

// TrySharedLease is AcquireSharedLease's single attempt, answering as
// TryLease does.
func TrySharedLease(path string) (*Lease, error) {
	return tryLease(path, true)
}

// acquireLease reopens the path on every poll, so a lock file replaced while
// it waits is the one it locks.
func acquireLease(ctx context.Context, path string, shared bool) (*Lease, error) {
	clean, err := validateAbsClean(path)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ticker := time.NewTicker(leasePollInterval)
	defer ticker.Stop()
	for {
		l, err := takeLease(clean, shared)
		if !errors.Is(err, errLeaseBusy) && !errors.Is(err, ErrRaced) {
			return l, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %s: %w", ErrLeaseHeld, clean, ctx.Err())
		case <-ticker.C:
		}
	}
}

func tryLease(path string, shared bool) (*Lease, error) {
	clean, err := validateAbsClean(path)
	if err != nil {
		return nil, err
	}
	l, err := takeLease(clean, shared)
	if errors.Is(err, errLeaseBusy) {
		return nil, fmt.Errorf("%w: %s", ErrLeaseHeld, clean)
	}
	return l, err
}

// Close releases the lease by closing its descriptor; the lock file stays.
// A second Close returns an error matching fs.ErrClosed.
func (l *Lease) Close() error {
	return l.f.Close()
}

// Covers reports whether the lock file this lease locked is still the one at
// its path. It is false when nothing is there or another file is (the file
// was removed, and perhaps made again); any other stat failure is the error,
// which is evidence of neither.
func (l *Lease) Covers() (bool, error) {
	held, err := l.f.Stat()
	if err != nil {
		return false, err
	}
	at, err := os.Lstat(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(held, at), nil
}
