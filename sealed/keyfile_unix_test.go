//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package sealed

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/filesafe"
)

// keyFileText returns the text of the vectors' key file named name.
func keyFileText(t *testing.T, name string) string {
	t.Helper()
	for _, kf := range loadVectors(t).KeyFiles {
		if kf.Name == name {
			return kf.Text
		}
	}
	t.Fatalf("the vectors hold no key file %q", name)
	return ""
}

// vectorIDs returns the vectors' key ids by key name.
func vectorIDs(t *testing.T) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for _, k := range loadVectors(t).Keys {
		ids[k.Name] = k.ID
	}
	return ids
}

// privateDir returns a new directory at 0700, the mode the key file's
// directory must have: t.TempDir's own is the umask's.
func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod the directory: %v", err)
	}
	return dir
}

// writeKeyFile writes text as dir/sealing.key at mode and returns its path.
func writeKeyFile(t *testing.T, dir, text string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, "sealing.key")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write the key file: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod the key file: %v", err)
	}
	return path
}

// openKeyFile opens path, failing the test on an error, and closes the
// KeyFile when the test ends.
func openKeyFile(t *testing.T, path string) *KeyFile {
	t.Helper()
	f, err := OpenKeyFile(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenKeyFile: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// checkNoLease checks that no KeyFile holds the lease of the key file at
// path: filesafe's TryLease, which answers ErrLeaseHeld while another
// holder has it, takes it at once and gives it back.
func checkNoLease(t *testing.T, path string) {
	t.Helper()
	l, err := filesafe.TryLease(path + lockSuffix)
	if err != nil {
		t.Errorf("TryLease: %v; want the lease free", err)
		return
	}
	if err := l.Close(); err != nil {
		t.Errorf("give the lease back: %v", err)
	}
}

// checkKeyless checks that OpenKeyFile answered a KeyFile holding no key
// and no lease, and closes it when the test ends.
func checkKeyless(t *testing.T, f *KeyFile, err error) {
	t.Helper()
	if err != nil || f == nil {
		t.Fatalf("OpenKeyFile = a KeyFile %v, %v; want a keyless KeyFile, nil", f != nil, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if f.src.lease != nil {
		t.Error("a keyless KeyFile holds the lease")
	}
	checkNoKey(t, f.Sealer())
}

func stubCheckPrivateDir(t *testing.T, fn func(dir string) error) {
	t.Helper()
	old := checkPrivateDir
	checkPrivateDir = fn
	t.Cleanup(func() { checkPrivateDir = old })
}

func stubAcquireLease(t *testing.T, fn func(ctx context.Context, lockPath string) (*filesafe.Lease, error)) {
	t.Helper()
	old := acquireLease
	acquireLease = fn
	t.Cleanup(func() { acquireLease = old })
}

func stubGeteuid(t *testing.T, euid int) {
	t.Helper()
	old := geteuid
	geteuid = func() int { return euid }
	t.Cleanup(func() { geteuid = old })
}

// checkRefusal checks that OpenKeyFile answered no KeyFile and an error
// matching each of is and none of isNot, naming name and holding no hex
// run a key line's id or key would leave.
func checkRefusal(t *testing.T, f *KeyFile, err error, name string, is, isNot []error) {
	t.Helper()
	if f != nil {
		_ = f.Close()
		t.Fatalf("OpenKeyFile = a KeyFile, %v; want none", err)
	}
	if err == nil {
		t.Fatal("OpenKeyFile = nil error")
	}
	for _, want := range is {
		if !errors.Is(err, want) {
			t.Errorf("%v does not match %v", err, want)
		}
	}
	for _, not := range isNot {
		if errors.Is(err, not) {
			t.Errorf("%v matches %v", err, not)
		}
	}
	if msg := err.Error(); !strings.Contains(msg, name) {
		t.Errorf("%q does not name %s", msg, name)
	}
	if hexRun.MatchString(err.Error()) {
		t.Errorf("%q holds a hex run of 32 or more", err)
	}
}

// TestOpenKeyFile checks OpenKeyFile on the vectors' two-key file, which
// Python's cryptography computed (docs/format.md): its Sealer holds K2
// current and K1 previous, opens the vectors' values, sealed under K1, with
// current false (the Open and OpenAtRest godoc), and seals under K2. A path
// that cleans to the file opens it (filesafe.ValidatePath cleans a path
// before its check), and ENOTDIR on a path component (lstat(2)) is an
// error, never a missing key file.
func TestOpenKeyFile(t *testing.T) {
	v := loadVectors(t)
	ids := vectorIDs(t)
	dir := privateDir(t)
	path := writeKeyFile(t, dir, keyFileText(t, "two keys"), 0o600)
	t.Run("two keys", func(t *testing.T) {
		s := openKeyFile(t, path).Sealer()
		if got, want := s.KeyIDs(), (KeyIDs{Current: ids["K2"], Previous: ids["K1"]}); got != want {
			t.Errorf("KeyIDs = %+v, want %+v", got, want)
		}
		purpose := Purpose(v.Values.Purpose)
		if p, cur, err := s.Open(purpose, v.Values.Expiring); err != nil || cur || string(p) != v.Values.Payload {
			t.Errorf("Open(the vectors' value) = %q, %v, %v; want %q, false, nil", p, cur, err, v.Values.Payload)
		}
		if p, cur, err := s.OpenAtRest(purpose, v.Values.AtRest); err != nil || cur || string(p) != v.Values.Payload {
			t.Errorf("OpenAtRest(the vectors' value) = %q, %v, %v; want %q, false, nil", p, cur, err, v.Values.Payload)
		}
		value, err := s.SealAtRest("example/open", []byte("x"))
		if err != nil {
			t.Fatalf("SealAtRest: %v", err)
		}
		if _, cur, err := New(v.key(t, "K2")).OpenAtRest("example/open", value); err != nil || !cur {
			t.Errorf("New(K2).OpenAtRest = %v, %v; want the value open under K2", cur, err)
		}
	})
	t.Run("uncleaned path", func(t *testing.T) {
		f := openKeyFile(t, dir+"/absent/../sealing.key")
		if got := f.Sealer().KeyIDs().Current; got != ids["K2"] {
			t.Errorf("KeyIDs().Current = %q, want K2's", got)
		}
		if _, err := os.Lstat(path + lockSuffix); err != nil {
			t.Errorf("the lock file beside the cleaned path: %v", err)
		}
	})
	t.Run("a path component is a file", func(t *testing.T) {
		under := filepath.Join(path, "sealing.key")
		f, err := OpenKeyFile(t.Context(), under)
		checkRefusal(t, f, err, under, []error{syscall.ENOTDIR}, []error{ErrUnsafeKeyFile, ErrKeyFileFormat})
	})
}

// TestOpenKeyFileMissing checks that an open never creates a key file (the
// OpenKeyFile godoc: "Where no file is, it creates nothing and returns a
// KeyFile holding no key"): no directory, no lock file and no lease, also
// for a key file or directory removed after OpenKeyFile found the file,
// before its directory check, after it, or before its read; and a done ctx
// does not fail it, since only a key file needs the lease.
func TestOpenKeyFileMissing(t *testing.T) {
	text := keyFileText(t, "one key")
	t.Run("no directory", func(t *testing.T) {
		dir := filepath.Join(privateDir(t), "keys")
		f, err := OpenKeyFile(t.Context(), filepath.Join(dir, "sealing.key"))
		checkKeyless(t, f, err)
		if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Lstat(the directory) = %v; want none made", err)
		}
	})
	t.Run("no key file", func(t *testing.T) {
		dir := privateDir(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		for _, c := range []context.Context{t.Context(), ctx} {
			f, err := OpenKeyFile(c, filepath.Join(dir, "sealing.key"))
			checkKeyless(t, f, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		for _, e := range entries {
			t.Errorf("OpenKeyFile made %s", e.Name())
		}
	})
	t.Run("key file gone by the read", func(t *testing.T) {
		path := writeKeyFile(t, privateDir(t), text, 0o600)
		stubCheckPrivateDir(t, func(dir string) error {
			if err := os.Remove(path); err != nil {
				t.Errorf("remove the key file: %v", err)
			}
			return filesafe.CheckPrivateDir(dir)
		})
		f, err := OpenKeyFile(t.Context(), path)
		checkKeyless(t, f, err)
		checkNoLease(t, path)
	})
	for _, tc := range []struct {
		name        string
		beforeCheck bool
	}{
		{"directory gone before its check", true},
		{"directory gone after its check", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(privateDir(t), "keys")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := writeKeyFile(t, dir, text, 0o600)
			stubCheckPrivateDir(t, func(d string) error {
				if tc.beforeCheck {
					if err := os.RemoveAll(dir); err != nil {
						t.Errorf("remove the directory: %v", err)
					}
					return filesafe.CheckPrivateDir(d)
				}
				err := filesafe.CheckPrivateDir(d)
				if rerr := os.RemoveAll(dir); rerr != nil {
					t.Errorf("remove the directory: %v", rerr)
				}
				return err
			})
			f, err := OpenKeyFile(t.Context(), path)
			checkKeyless(t, f, err)
			if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Lstat(the directory) = %v; want it not made again", err)
			}
		})
	}
}

// TestUnsafeKeyFile checks each refusal of a key file another user could
// read or replace (the ErrUnsafeKeyFile godoc), from the open descriptor's
// fstat (inode(7): the owner, the permission bits, setuid, setgid and
// sticky), filesafe.OpenRegularNoFollow (a final symlink, anything not a
// regular file, a FIFO without blocking: open(2) O_NOFOLLOW, O_NONBLOCK) and
// filesafe.CheckPrivateDir (a directory with group or other access, or a
// symlink). Each names the key file, and the lease taken for the read is
// given back; a 0400 file is within 0600 and opens.
func TestUnsafeKeyFile(t *testing.T) {
	text := keyFileText(t, "one key")
	modeFile := func(mode os.FileMode) func(t *testing.T, dir string) string {
		return func(t *testing.T, dir string) string { return writeKeyFile(t, dir, text, mode) }
	}
	for _, tc := range []struct {
		name string
		make func(t *testing.T, dir string) string
		is   error
		says string
	}{
		{"mode 0640", modeFile(0o640), ErrUnsafeKeyFile, "mode 0640"},
		{"mode 0604", modeFile(0o604), ErrUnsafeKeyFile, "mode 0604"},
		{"mode 0700", modeFile(0o700), ErrUnsafeKeyFile, "mode 0700"},
		{"mode 04600", modeFile(0o600 | os.ModeSetuid), ErrUnsafeKeyFile, "mode 04600"},
		{"mode 01600", modeFile(0o600 | os.ModeSticky), ErrUnsafeKeyFile, "mode 01600"},
		{"symlink", func(t *testing.T, dir string) string {
			real := writeKeyFile(t, dir, text, 0o600)
			link := filepath.Join(dir, "link.key")
			if err := os.Symlink(real, link); err != nil {
				t.Fatal(err)
			}
			return link
		}, filesafe.ErrSymlinkTarget, ""},
		{"FIFO", func(t *testing.T, dir string) string {
			path := filepath.Join(dir, "sealing.key")
			if err := mkfifo(path); err != nil {
				t.Fatal(err)
			}
			return path
		}, filesafe.ErrNotRegular, ""},
		{"directory 0755", func(t *testing.T, dir string) string {
			keys := filepath.Join(dir, "keys")
			if err := os.Mkdir(keys, 0o700); err != nil {
				t.Fatal(err)
			}
			path := writeKeyFile(t, keys, text, 0o600)
			if err := os.Chmod(keys, 0o755); err != nil {
				t.Fatal(err)
			}
			return path
		}, filesafe.ErrModeTooOpen, ""},
		{"directory a symlink", func(t *testing.T, dir string) string {
			keys := filepath.Join(dir, "keys")
			if err := os.Mkdir(keys, 0o700); err != nil {
				t.Fatal(err)
			}
			writeKeyFile(t, keys, text, 0o600)
			link := filepath.Join(dir, "link")
			if err := os.Symlink(keys, link); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(link, "sealing.key")
		}, filesafe.ErrSymlinkTarget, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.make(t, privateDir(t))
			f, err := OpenKeyFile(t.Context(), path)
			checkRefusal(t, f, err, path, []error{ErrUnsafeKeyFile, tc.is}, []error{ErrKeyFileFormat})
			if err != nil && !strings.Contains(err.Error(), tc.says) {
				t.Errorf("%q does not say %q", err, tc.says)
			}
			checkNoLease(t, path)
		})
	}
	t.Run("another owner", func(t *testing.T) {
		path := writeKeyFile(t, privateDir(t), text, 0o600)
		stubGeteuid(t, os.Geteuid()+1)
		f, err := OpenKeyFile(t.Context(), path)
		checkRefusal(t, f, err, path, []error{ErrUnsafeKeyFile}, nil)
		if want := fmt.Sprintf("owned by uid %d", os.Geteuid()); err != nil && !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
		checkNoLease(t, path)
	})
	t.Run("mode 0400", func(t *testing.T) {
		f := openKeyFile(t, writeKeyFile(t, privateDir(t), text, 0o400))
		if got := f.Sealer().KeyIDs().Current; got != vectorIDs(t)["K1"] {
			t.Errorf("KeyIDs().Current = %q, want K1's", got)
		}
	})
}

// TestKeyFileReadBound checks the ErrKeyFileFormat godoc's "or over 512
// bytes" at its edge: a 512-byte file reaches the parser, which refuses its
// first line, and a 513-byte one is refused by filesafe.ReadBoundedFile's
// ErrFileTooLarge ("if the file exceeds maxBytes") before any parse. Both
// name the key file, and the lease taken for the read is given back.
func TestKeyFileReadBound(t *testing.T) {
	for _, tc := range []struct {
		name    string
		size    int
		is, not error
	}{
		{"512 bytes", 512, ErrKeyFileFormat, filesafe.ErrFileTooLarge},
		{"513 bytes", 513, filesafe.ErrFileTooLarge, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeKeyFile(t, privateDir(t), strings.Repeat("x", tc.size-1)+"\n", 0o600)
			f, err := OpenKeyFile(t.Context(), path)
			not := []error{ErrUnsafeKeyFile}
			if tc.not != nil {
				not = append(not, tc.not)
			}
			checkRefusal(t, f, err, path, []error{ErrKeyFileFormat, tc.is}, not)
			checkNoLease(t, path)
		})
	}
}

// TestLease checks that a KeyFile holding a key holds its lease from
// OpenKeyFile to Close (the KeyFile and Close godoc): flock(2) locks belong
// to an open file description, so a second OpenKeyFile in this process
// waits, answering ErrKeyFileInUse with filesafe.ErrLeaseHeld and the
// context's error at its deadline; one with no deadline takes the lease
// within one of AcquireLease's 50 ms polls of Close; the closed Sealer holds
// no key, a second Close answers nil, and the lock file stays. A done ctx
// opens nothing: its error, not ErrKeyFileInUse.
func TestLease(t *testing.T) {
	path := writeKeyFile(t, privateDir(t), keyFileText(t, "one key"), 0o600)
	lockPath := path + lockSuffix
	t.Run("held", func(t *testing.T) {
		openKeyFile(t, path)
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		f, err := OpenKeyFile(ctx, path)
		checkRefusal(t, f, err, lockPath, []error{ErrKeyFileInUse, filesafe.ErrLeaseHeld, context.DeadlineExceeded}, []error{ErrUnsafeKeyFile})
	})
	t.Run("released at Close", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			a, err := OpenKeyFile(t.Context(), path)
			if err != nil {
				t.Fatalf("OpenKeyFile: %v", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type opened struct {
				f   *KeyFile
				err error
			}
			done := make(chan opened, 1)
			go func() {
				f, err := OpenKeyFile(ctx, path)
				done <- opened{f, err}
			}()
			time.Sleep(time.Second)
			select {
			case r := <-done:
				t.Fatalf("a second OpenKeyFile returned while the first held the lease: %v", r.err)
			default:
			}
			closed := time.Now()
			if err := a.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			select {
			case r := <-done:
				if r.err != nil {
					t.Fatalf("the waiting OpenKeyFile: %v", r.err)
				}
				if waited := time.Since(closed); waited > 50*time.Millisecond {
					t.Errorf("the waiting OpenKeyFile took the lease %v after Close, over one 50 ms poll", waited)
				}
				if got := r.f.Sealer().KeyIDs().Current; got != vectorIDs(t)["K1"] {
					t.Errorf("the second KeyFile's KeyIDs().Current = %q, want K1's", got)
				}
				if err := r.f.Close(); err != nil {
					t.Errorf("Close the second KeyFile: %v", err)
				}
			case <-time.After(time.Second):
				t.Error("a second OpenKeyFile still waits 1s after Close")
				cancel()
				<-done
			}
			checkNoKey(t, a.Sealer())
			if err := a.Close(); err != nil {
				t.Errorf("second Close = %v, want nil", err)
			}
		})
		if _, err := os.Lstat(lockPath); err != nil {
			t.Errorf("the lock file after Close: %v; want it kept", err)
		}
	})
	t.Run("done context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		f, err := OpenKeyFile(ctx, path)
		checkRefusal(t, f, err, lockPath, []error{context.Canceled}, []error{ErrKeyFileInUse, ErrUnsafeKeyFile})
	})
}

// TestLockFileRefused checks that each unsafe thing at <path>.lock an
// unprivileged test can make is ErrUnsafeKeyFile wrapping filesafe's
// AcquireLease refusal (its godoc: "a symlink (ErrSymlinkTarget), a
// non-regular file (ErrNotRegular) ... group or other access
// (ErrModeTooOpen)"), naming the lock file, before the key file is read:
// the key file is no key file, which a read would answer ErrKeyFileFormat.
// A FIFO does not block the open (open(2) O_NONBLOCK), and a lock file's
// wide mode is left as found.
func TestLockFileRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(t *testing.T, lockPath string)
		is   error
		mode os.FileMode
	}{
		{"symlink", func(t *testing.T, lockPath string) {
			target := lockPath + ".target"
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, lockPath); err != nil {
				t.Fatal(err)
			}
		}, filesafe.ErrSymlinkTarget, 0},
		{"directory", func(t *testing.T, lockPath string) {
			if err := os.Mkdir(lockPath, 0o700); err != nil {
				t.Fatal(err)
			}
		}, filesafe.ErrNotRegular, 0},
		{"unix socket", func(t *testing.T, lockPath string) {
			l, err := net.Listen("unix", lockPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = l.Close() })
		}, filesafe.ErrNotRegular, 0},
		{"FIFO", func(t *testing.T, lockPath string) {
			if err := mkfifo(lockPath); err != nil {
				t.Fatal(err)
			}
		}, filesafe.ErrNotRegular, 0},
		{"mode 0644", wideLockFile(0o644), filesafe.ErrModeTooOpen, 0o644},
		{"mode 0640", wideLockFile(0o640), filesafe.ErrModeTooOpen, 0o640},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeKeyFile(t, privateDir(t), "not a key file\n", 0o600)
			lockPath := path + lockSuffix
			tc.make(t, lockPath)
			f, err := OpenKeyFile(t.Context(), path)
			checkRefusal(t, f, err, lockPath, []error{ErrUnsafeKeyFile, tc.is}, []error{ErrKeyFileFormat, ErrKeyFileInUse})
			if tc.mode == 0 {
				return
			}
			fi, err := os.Lstat(lockPath)
			if err != nil {
				t.Fatalf("Lstat(the lock file): %v", err)
			}
			if got := fi.Mode().Perm(); got != tc.mode {
				t.Errorf("the lock file's mode after the refusal = %#o, want %#o kept", got, tc.mode)
			}
		})
	}
}

// wideLockFile makes an empty lock file at mode.
func wideLockFile(mode os.FileMode) func(t *testing.T, lockPath string) {
	return func(t *testing.T, lockPath string) {
		if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(lockPath, mode); err != nil {
			t.Fatal(err)
		}
	}
}

// leaseCtxKey marks the context a test passes OpenKeyFile, so the
// acquireLease seam can see it was handed the caller's.
type leaseCtxKey struct{}

// TestLeaseAnswers checks, through the acquireLease seam, how OpenKeyFile
// answers each error filesafe's AcquireLease and ErrLeaseHeld godoc name,
// those an unprivileged test cannot make included: ErrLeaseHeld is
// ErrKeyFileInUse; a bare context error is itself; each refusal is
// ErrUnsafeKeyFile; ErrModeNotStored and a failed flock(2) (ENOLCK) are
// themselves; fs.ErrNotExist is a missing key file only when the key file
// is gone too. Each call is handed the caller's ctx and path+".lock", and
// each error names the lock file.
func TestLeaseAnswers(t *testing.T) {
	text := keyFileText(t, "one key")
	for _, tc := range []struct {
		name   string
		answer func(lockPath string) error
		is     []error
		isNot  []error
	}{
		{"lease held", func(p string) error {
			return fmt.Errorf("%w: %s: %w", filesafe.ErrLeaseHeld, p, context.DeadlineExceeded)
		}, []error{ErrKeyFileInUse, filesafe.ErrLeaseHeld, context.DeadlineExceeded}, []error{ErrUnsafeKeyFile}},
		{"bare context error", func(string) error {
			return context.DeadlineExceeded
		}, []error{context.DeadlineExceeded}, []error{ErrKeyFileInUse, ErrUnsafeKeyFile}},
		{"symlink", func(p string) error {
			return fmt.Errorf("%w: %s: %w", filesafe.ErrSymlinkTarget, p, syscall.ELOOP)
		}, []error{ErrUnsafeKeyFile, filesafe.ErrSymlinkTarget}, []error{ErrKeyFileInUse}},
		{"not regular", func(p string) error {
			return &filesafe.NotRegularError{Name: p, Mode: fs.ModeDir | 0o700}
		}, []error{ErrUnsafeKeyFile, filesafe.ErrNotRegular}, []error{ErrKeyFileInUse}},
		{"not owned", func(p string) error {
			return fmt.Errorf("%w: %s: owned by uid 0, want the effective uid 1000", filesafe.ErrNotOwned, p)
		}, []error{ErrUnsafeKeyFile, filesafe.ErrNotOwned}, []error{ErrKeyFileInUse}},
		{"mode too open", func(p string) error {
			return fmt.Errorf("%w: %s (mode 0644)", filesafe.ErrModeTooOpen, p)
		}, []error{ErrUnsafeKeyFile, filesafe.ErrModeTooOpen}, []error{ErrKeyFileInUse}},
		{"mode not stored", func(p string) error {
			return fmt.Errorf("%w: %s: asked for 0600, filesystem stored 0644", filesafe.ErrModeNotStored, p)
		}, []error{filesafe.ErrModeNotStored}, []error{ErrUnsafeKeyFile, ErrKeyFileInUse}},
		{"flock ENOLCK", func(p string) error {
			return &fs.PathError{Op: "flock", Path: p, Err: syscall.ENOLCK}
		}, []error{syscall.ENOLCK}, []error{ErrUnsafeKeyFile, ErrKeyFileInUse}},
		{"lock path missing, key file there", func(p string) error {
			return &fs.PathError{Op: "open", Path: p, Err: syscall.ENOENT}
		}, []error{fs.ErrNotExist}, []error{ErrUnsafeKeyFile, ErrKeyFileInUse}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeKeyFile(t, privateDir(t), text, 0o600)
			lockPath := path + lockSuffix
			stubAcquireLease(t, func(ctx context.Context, got string) (*filesafe.Lease, error) {
				if got != lockPath {
					t.Errorf("acquireLease(%q), want %q", got, lockPath)
				}
				if ctx.Value(leaseCtxKey{}) == nil {
					t.Error("acquireLease was not handed the caller's ctx")
				}
				return nil, tc.answer(got)
			})
			f, err := OpenKeyFile(context.WithValue(t.Context(), leaseCtxKey{}, true), path)
			checkRefusal(t, f, err, lockPath, tc.is, tc.isNot)
		})
	}
	t.Run("lock path missing, key file gone", func(t *testing.T) {
		path := writeKeyFile(t, privateDir(t), text, 0o600)
		stubAcquireLease(t, func(_ context.Context, lockPath string) (*filesafe.Lease, error) {
			if err := os.Remove(path); err != nil {
				t.Errorf("remove the key file: %v", err)
			}
			return nil, &fs.PathError{Op: "open", Path: lockPath, Err: syscall.ENOENT}
		})
		f, err := OpenKeyFile(t.Context(), path)
		checkKeyless(t, f, err)
	})
}
