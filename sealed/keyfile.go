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

// errClosed is the cause Close gives a waiting Create's lease wait and the
// answer of every write once Close began, so context.Cause tells Close
// apart from the caller's own deadline.
var errClosed = fmt.Errorf("sealed: %w", fs.ErrClosed)

// errNoPath is every write's answer on the zero KeyFile.
var errNoPath = fmt.Errorf("sealed: key file path: %w", filesafe.ErrEmptyPath)

// The seams over filesafe, so a test reaches answers an unprivileged
// process cannot make: another user's lock file, a mode the filesystem did
// not store, a removed file or directory between two steps, a write not
// durable or held while Close runs, a failed parent sync or lookup.
var (
	acquireLease     = filesafe.AcquireLease
	checkPrivateDir  = filesafe.CheckPrivateDir
	ensurePrivateDir = filesafe.EnsurePrivateDir
	leaseCovers      = (*filesafe.Lease).Covers
	writeFile        = filesafe.WriteFile
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
	// stopWait ends the lease wait of a running Create; set under closeMu.
	stopWait context.CancelCauseFunc
	path     string
	// pending names the keys of a file f wrote and did not install. It is
	// valid only while the lease that covered that write is held: no other
	// KeyFile can have sealed under them, so a retry may write over it.
	pending KeyIDs
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

// dirErr maps an EnsurePrivateDir error for the key directory dir: a
// failed parent sync leaves the directory's entry where a power loss can
// take it, with any key file in it.
func dirErr(dir string, err error) error {
	if errors.Is(err, filesafe.ErrParentNotSynced) {
		return fmt.Errorf("%w: %s: %w", ErrNotDurable, dir, err)
	}
	return pathErr(dir, err)
}

// releaseLease gives up a lease. Its error says nothing to act on:
// close(2) frees the descriptor even when it reports one, and with it the
// flock.
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
		if f.stopWait != nil {
			f.stopWait(errClosed)
		}
	}
	f.closeMu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	// The ring goes first, so no Sealer seals under a key once the lease,
	// and with it any guard against a later holder's Retire, is gone.
	f.src.ring.Store(nil)
	f.pending = KeyIDs{}
	lease := f.src.lease
	if lease == nil {
		return nil
	}
	f.src.lease = nil
	if err := lease.Close(); err != nil {
		return fmt.Errorf("sealed: %s: %w", f.lockPath(), err)
	}
	return nil
}

// Create writes a key file holding one new random key where nothing is at
// the path, and installs it in f once filesafe reports the file durable. It
// makes a missing directory, one level, at 0700, takes the lease, and writes
// the file at 0600. It answers ErrKeyExists, writing nothing, when f holds a
// key or a file is at the path, except a file f wrote and did not install
// (ErrNotDurable): a retry writes a new key over that one. A new key opens
// nothing sealed before it, so an app creates one at its first start, or
// when it gives up what a lost key file sealed.
func (f *KeyFile) Create(ctx context.Context) error {
	if f.path == "" {
		return errNoPath
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	err := f.create(ctx)
	// A keyless holder would keep every other KeyFile off the path, so the
	// lease stays only beside a key or the record of f's own write.
	if f.src.lease != nil && f.src.load() == nil && f.pending == (KeyIDs{}) {
		f.dropLease()
	}
	return err
}

// create is Create under f.mu, before the lease rule.
func (f *KeyFile) create(ctx context.Context) error {
	if f.closingBegun() {
		return f.closedErr()
	}
	if f.src.load() != nil {
		return fmt.Errorf("%w: %s", ErrKeyExists, f.path)
	}
	dir := filepath.Dir(f.path)
	_, derr := ensurePrivateDir(dir)
	if f.src.lease != nil {
		covered, err := leaseCovers(f.src.lease)
		switch {
		case err != nil:
			// A failed lookup shows neither that the recorded write went
			// nor that it stayed, so the lease and the record stay.
			if derr != nil {
				return dirErr(dir, derr)
			}
			return fmt.Errorf("sealed: %s: %w", f.lockPath(), err)
		case !covered:
			// The directory was removed, with the recorded write and the
			// lock file the lease locked.
			f.dropLease()
		}
	}
	if derr != nil {
		return dirErr(dir, derr)
	}
	if f.src.lease == nil {
		lease, err := f.takeLease(ctx)
		if err != nil {
			return err
		}
		f.src.lease = lease
	}
	if err := f.checkAbsent(ctx); err != nil {
		return err
	}
	rk, err := newRingKey(randomKey())
	if err != nil {
		return fmt.Errorf("sealed: %w", err)
	}
	return f.write(ctx, &ring{keys: [2]ringKey{rk}, n: 1})
}

// takeLease waits for the lease on f's lock file under a context of its
// own, whose cancel function Close calls, so a shutdown never waits on
// another holder whatever ctx the caller passed.
func (f *KeyFile) takeLease(ctx context.Context) (*filesafe.Lease, error) {
	leaseCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	// The check and the registration are one step: a Close either finds
	// stopWait set or is found here.
	f.closeMu.Lock()
	if f.closingBegun() {
		f.closeMu.Unlock()
		return nil, f.closedErr()
	}
	f.stopWait = cancel
	f.closeMu.Unlock()
	lease, err := acquireLease(leaseCtx, f.lockPath())
	f.closeMu.Lock()
	f.stopWait = nil
	f.closeMu.Unlock()
	if err == nil {
		return lease, nil
	}
	// Whatever AcquireLease answered once Close ended its wait, a bare
	// context error or ErrLeaseHeld included, the answer is Close's.
	if errors.Is(context.Cause(leaseCtx), errClosed) {
		return nil, f.closedErr()
	}
	return nil, leaseErr(f.lockPath(), err)
}

// checkAbsent answers nil when nothing is at f's path, or the file f wrote
// and did not install. Anything else at the path is ErrKeyExists and drops
// the record, which no longer names what is there.
func (f *KeyFile) checkAbsent(ctx context.Context) error {
	r, err := readKeyFile(ctx, f.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err == nil && r.ids() == f.pending:
		return nil
	case err == nil:
		f.pending = KeyIDs{}
		return fmt.Errorf("%w: %s", ErrKeyExists, f.path)
	}
	if _, lerr := os.Lstat(f.path); lerr != nil {
		// Nothing shows a file is there: the read's own error answers.
		return err
	}
	f.pending = KeyIDs{}
	return fmt.Errorf("%w: %w", ErrKeyExists, err)
}

// Rotate makes a new random key current and keeps the current key as the
// previous one, which still opens values with current false, so the caller
// seals each again, then calls Retire. The Sealer f handed out seals under
// the new key once Rotate returns, so an app keeps it; only a Subkey, a copy
// that keeps the old key, is taken again. It answers ErrNoKey when f
// holds no key, ErrRotationPending while a previous key is held, and
// ErrKeyFileChanged or ErrNotDurable as their docs say.
func (f *KeyFile) Rotate(ctx context.Context) error {
	return f.rewrite(ctx, func(r *ring) (*ring, error) {
		if r.n == 2 {
			return nil, ErrRotationPending
		}
		rk, err := newRingKey(randomKey())
		if err != nil {
			return nil, fmt.Errorf("sealed: %w", err)
		}
		return &ring{keys: [2]ringKey{rk, r.keys[0]}, n: 2}, nil
	})
}

// Retire removes the previous key from the file and from f, so f's Sealer
// no longer opens what was sealed under it; a Subkey of that key taken
// earlier is a copy Retire cannot reach, so rebuild what holds it, or
// restart. It is never automatic: call it once every such value is sealed
// again. With no previous key it writes nothing and answers nil. Its errors
// are Rotate's, less ErrRotationPending.
func (f *KeyFile) Retire(ctx context.Context) error {
	return f.rewrite(ctx, func(r *ring) (*ring, error) {
		if r.n == 1 {
			return nil, nil
		}
		return &ring{keys: [2]ringKey{r.keys[0]}, n: 1}, nil
	})
}

// rewrite runs Rotate or Retire: once the path is shown to hold what f
// installed or wrote, it writes next of the installed ring, never of the
// file read, so a retry over f's uninstalled write runs as if the failed
// call had not. A nil ring from next writes nothing.
func (f *KeyFile) rewrite(ctx context.Context, next func(*ring) (*ring, error)) error {
	if f.path == "" {
		return errNoPath
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closingBegun() {
		return f.closedErr()
	}
	installed := f.src.load()
	if installed == nil {
		return ErrNoKey
	}
	if err := f.checkUnchanged(ctx, installed); err != nil {
		return err
	}
	r, err := next(installed)
	if r == nil {
		return err
	}
	return f.write(ctx, r)
}

// checkUnchanged answers nil when f's lease still covers the path and the
// key file holds the keys f installed or its uninstalled write; otherwise
// ErrKeyFileChanged, dropping the record, or the error of a step that
// could not tell, keeping it.
func (f *KeyFile) checkUnchanged(ctx context.Context, installed *ring) error {
	covered, err := leaseCovers(f.src.lease)
	if err != nil {
		return fmt.Errorf("sealed: %s: %w", f.lockPath(), err)
	}
	if !covered {
		f.pending = KeyIDs{}
		return fmt.Errorf("%w: %s: not the lock file this KeyFile locked", ErrKeyFileChanged, f.lockPath())
	}
	r, err := readKeyFile(ctx, f.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f.pending = KeyIDs{}
		return fmt.Errorf("%w: %s: missing", ErrKeyFileChanged, f.path)
	case err != nil:
		return err
	}
	if ids := r.ids(); ids != installed.ids() && ids != f.pending {
		f.pending = KeyIDs{}
		return fmt.Errorf("%w: %s: holds other keys", ErrKeyFileChanged, f.path)
	}
	return nil
}

// write publishes r at f's path and installs it only once filesafe reports
// the file durable. A file it could not report durable may be what a power
// loss leaves, or not, so nothing is installed and its key ids are kept as
// the record a retry may write over.
func (f *KeyFile) write(ctx context.Context, r *ring) error {
	if f.closingBegun() {
		return f.closedErr()
	}
	res, err := writeFile(ctx, f.path, encodeKeyFile(r), filesafe.WithMode(0o600))
	switch {
	case err != nil:
		return fmt.Errorf("sealed: %s: %w", f.path, err)
	case !res.Durable:
		f.pending = r.ids()
		return fmt.Errorf("%w: %s", ErrNotDurable, f.path)
	}
	f.pending = KeyIDs{}
	f.src.ring.Store(r)
	return nil
}

// dropLease releases f's lease, and with it the record, which only that
// lease kept valid.
func (f *KeyFile) dropLease() {
	releaseLease(f.src.lease)
	f.src.lease = nil
	f.pending = KeyIDs{}
}

// closingBegun reports whether Close has begun on f.
func (f *KeyFile) closingBegun() bool {
	select {
	case <-f.closing:
		return true
	default:
		return false
	}
}

func (f *KeyFile) closedErr() error { return fmt.Errorf("%w: %s", errClosed, f.path) }

func (f *KeyFile) lockPath() string { return f.path + lockSuffix }
