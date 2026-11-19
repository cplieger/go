package sealed

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/cplieger/go/filesafe"
)

// maxKeyFileLen bounds a key file read: a two-key file is 227 bytes.
const maxKeyFileLen = 512

// lockSuffix names the lock file beside a key file, whose lease the
// KeyFile holds. The key file itself cannot carry it: each write renames a
// new file into its place, which leaves a lock on the replaced inode.
const lockSuffix = ".lock"

// The seams over filesafe, so a test reaches answers an unprivileged
// process cannot make: another user's lock file, a mode the filesystem did
// not store, a key file or directory removed between two steps.
var (
	acquireLease    = filesafe.AcquireLease
	checkPrivateDir = filesafe.CheckPrivateDir
)

// refusals are filesafe's verdicts that another user could read or replace
// what a path names, which this package answers as ErrUnsafeKeyFile.
var refusals = [...]error{
	filesafe.ErrSymlinkTarget, filesafe.ErrNotRegular, filesafe.ErrNotDirectory,
	filesafe.ErrNotOwned, filesafe.ErrModeTooOpen,
}

// KeyFile is the key file at one path: a current key and, between Rotate
// and Retire, the previous one. Build one with OpenKeyFile. While it holds a
// key, or a file Create wrote and could not install (ErrNotDurable), it
// holds the file's lease, so its writes run in the process serving the file,
// and an admin command reaches that process instead of opening the file.
// Its methods are safe for concurrent use. The zero KeyFile has no path:
// its Sealer holds no key, its writes fail with filesafe.ErrEmptyPath, and
// its Close answers nil.
type KeyFile struct {
	src *keySource
	// closing is closed by the first Close, under closeMu.
	closing chan struct{}
	path    string
	// mu serializes the writes and Close; it is taken before closeMu.
	mu      sync.Mutex
	closeMu sync.Mutex
}

// OpenKeyFile reads the key file at path, an absolute path. Where no file
// is, it creates nothing and returns a KeyFile holding no key, whose Sealer
// answers ErrNoKey until Create. Otherwise it first takes the lease on
// path+".lock", creating that empty file if needed and waiting until ctx
// ends (ErrKeyFileInUse) while another KeyFile holds it. It refuses a file
// another user could read or replace (ErrUnsafeKeyFile) and one this
// release does not read (ErrKeyFileFormat); on any error a caller that must
// keep running hands its stores a nil *Sealer, which answers ErrNoKey.
func OpenKeyFile(ctx context.Context, path string) (*KeyFile, error) {
	if err := filesafe.ValidatePath(path); err != nil {
		return nil, fmt.Errorf("sealed: key file path: %w", err)
	}
	path = filepath.Clean(path)
	if platformRefusal != "" {
		return nil, unsupportedErr(path)
	}
	r, lease, err := openKeys(ctx, path)
	if err != nil {
		return nil, err
	}
	src := &keySource{lease: lease}
	src.ring.Store(r)
	return &KeyFile{path: path, src: src, closing: make(chan struct{})}, nil
}

// openKeys is OpenKeyFile's sequence after the platform check. It answers
// no ring and no lease where nothing is at path, or nothing is there by
// the step that looks: a keyless KeyFile never holds the lease.
func openKeys(ctx context.Context, path string) (*ring, *filesafe.Lease, error) {
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, pathErr(path, err)
	}
	if err := checkPrivateDir(filepath.Dir(path)); err != nil {
		return nil, nil, unlessGone(path, err, pathErr(path, err))
	}
	lockPath := path + lockSuffix
	lease, err := acquireLease(ctx, lockPath)
	if err != nil {
		return nil, nil, unlessGone(path, err, leaseErr(lockPath, err))
	}
	r, err := readKeyFile(ctx, path)
	if err != nil {
		releaseLease(lease)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	return r, lease, nil
}

// unlessGone answers nil, a missing key file, when a step's err says a
// name on the way to path is missing and a second Lstat finds nothing at
// path: the key file or its directory went after the first. Otherwise it
// answers mapped, the step's own error.
func unlessGone(path string, err, mapped error) error {
	if errors.Is(err, fs.ErrNotExist) {
		if _, lerr := os.Lstat(path); errors.Is(lerr, fs.ErrNotExist) {
			return nil
		}
	}
	return mapped
}

// readKeyFile is the read sequence of every open and write: path opened
// without following a final symlink, the open descriptor's owner and mode,
// then at most maxKeyFileLen bytes of that descriptor, parsed. A missing
// file is an error matching fs.ErrNotExist.
func readKeyFile(ctx context.Context, path string) (*ring, error) {
	f, fi, err := filesafe.OpenRegularNoFollow(path)
	if err != nil {
		return nil, pathErr(path, err)
	}
	defer func() { _ = f.Close() }() // opened read-only: a failed close loses nothing
	if oerr := ownerModeErr(path, fi); oerr != nil {
		return nil, oerr
	}
	data, err := filesafe.ReadBoundedFile(ctx, f, maxKeyFileLen)
	switch {
	case errors.Is(err, filesafe.ErrFileTooLarge):
		return nil, fmt.Errorf("%w: %s: %w", ErrKeyFileFormat, path, err)
	case err != nil:
		return nil, pathErr(path, err)
	}
	return parseKeyFile(path, data)
}

// leaseErr maps an AcquireLease error for the lock file at lockPath.
func leaseErr(lockPath string, err error) error {
	if errors.Is(err, filesafe.ErrLeaseHeld) {
		return fmt.Errorf("%w: %s: %w", ErrKeyFileInUse, lockPath, err)
	}
	return pathErr(lockPath, err)
}

// pathErr wraps err with name, as ErrUnsafeKeyFile when it is one of
// filesafe's refusals.
func pathErr(name string, err error) error {
	for _, r := range refusals {
		if errors.Is(err, r) {
			return fmt.Errorf("%w: %s: %w", ErrUnsafeKeyFile, name, err)
		}
	}
	return fmt.Errorf("sealed: %s: %w", name, err)
}

// releaseLease gives up a lease taken for a read that failed. Its error
// says nothing to act on: close(2) frees the descriptor even when it
// reports one, and with it the flock.
func releaseLease(l *filesafe.Lease) { _ = l.Close() }

// unsupportedErr refuses a key file on a system without file owners, modes
// or flock(2), where no check can keep another user out.
func unsupportedErr(path string) error {
	return fmt.Errorf("%w: %s: %s: %w", ErrUnsafeKeyFile, path, platformRefusal, filesafe.ErrModeUnsupported)
}

// Sealer returns the Sealer of f's keys. It reads them at each call, so it
// seals under the key Create or Rotate installs from the moment they return,
// and an app keeps the one Sealer for the process's life; it holds no key
// once f is closed.
func (f *KeyFile) Sealer() *Sealer { return &Sealer{src: f.src} }

// Close releases f's lease and its keys: f's Sealer answers ErrNoKey, and
// f's writes fail with fs.ErrClosed. It waits only for a key-file write
// already running, until filesafe returns: that write's ctx is checked
// between filesafe's steps, not inside a sync or rename the kernel holds. A
// Create, Rotate or Retire that has not begun its write answers fs.ErrClosed
// and writes nothing, and a Create waiting for the lease stops at once.
// Close it last, after the stores that use its Sealer. A second Close
// answers nil.
func (f *KeyFile) Close() error {
	if f.path == "" {
		return nil
	}
	f.closeMu.Lock()
	select {
	case <-f.closing:
	default:
		close(f.closing)
	}
	f.closeMu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	// The ring goes first, so no Sealer seals under a key once the lease,
	// and with it any guard against a later holder's Retire, is gone.
	f.src.ring.Store(nil)
	lease := f.src.lease
	if lease == nil {
		return nil
	}
	f.src.lease = nil
	if err := lease.Close(); err != nil {
		return fmt.Errorf("sealed: %s: %w", f.path+lockSuffix, err)
	}
	return nil
}
