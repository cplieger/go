//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package sealed

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
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

// openKeyless opens path, where nothing is, as a keyless KeyFile, and
// closes it when the test ends.
func openKeyless(t *testing.T, path string) *KeyFile {
	t.Helper()
	f, err := OpenKeyFile(t.Context(), path)
	checkKeyless(t, f, err)
	return f
}

// createKeyFile opens path, where nothing is, and creates its key file.
func createKeyFile(t *testing.T, path string) *KeyFile {
	t.Helper()
	f := openKeyless(t, path)
	if err := f.Create(t.Context()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return f
}

// checkInUse checks that another KeyFile holds the lease of the key file
// at path: an OpenKeyFile with a 100 ms context answers ErrKeyFileInUse.
func checkInUse(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	f, err := OpenKeyFile(ctx, path)
	if f != nil {
		_ = f.Close()
	}
	if !errors.Is(err, ErrKeyFileInUse) {
		t.Errorf("a second OpenKeyFile = %v; want ErrKeyFileInUse while the lease is held", err)
	}
}

// keyLine is one key line of a key file.
type keyLine struct {
	word, id string
	key      [keyLen]byte
}

var keyLineRE = regexp.MustCompile(`^(current|previous) ([0-9a-f]{32}) ([0-9a-f]{64})$`)

// readKeyLines reads the key file at path with this test's own reading of
// docs/format.md's grammar: the version line, then key lines, each ending
// in LF, each id the RFC 5869 §2.3 transcription's id of its key.
func readKeyLines(t *testing.T, path string) []keyLine {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the key file: %v", err)
	}
	text, ok := strings.CutSuffix(string(data), "\n")
	lines := strings.Split(text, "\n")
	if !ok || lines[0] != "sealed-key/v1" {
		t.Fatalf("the key file %q is not version 1 text ending in LF", data)
	}
	var keys []keyLine
	for _, line := range lines[1:] {
		m := keyLineRE.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("the key file holds the line %q, not a key line", line)
		}
		k := keyLine{word: m[1], id: m[2], key: [keyLen]byte(fromHex(m[3]))}
		if want := hex.EncodeToString(refHKDF(k.key[:], nil, "sealed/key-id", idLen)); k.id != want {
			t.Errorf("the %s key's id = %s, want %s", k.word, k.id, want)
		}
		keys = append(keys, k)
	}
	return keys
}

// lineIDs returns the KeyIDs of a key file's lines.
func lineIDs(lines []keyLine) KeyIDs {
	var ids KeyIDs
	if len(lines) > 0 {
		ids.Current = lines[0].id
	}
	if len(lines) > 1 {
		ids.Previous = lines[1].id
	}
	return ids
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

func stubEnsurePrivateDir(t *testing.T, fn func(dir string, opts ...filesafe.PrivateDirOption) (filesafe.PrivateDirResult, error)) {
	t.Helper()
	old := ensurePrivateDir
	ensurePrivateDir = fn
	t.Cleanup(func() { ensurePrivateDir = old })
}

func stubLeaseCovers(t *testing.T, fn func(l *filesafe.Lease) (bool, error)) {
	t.Helper()
	old := leaseCovers
	leaseCovers = fn
	t.Cleanup(func() { leaseCovers = old })
}

func stubWriteFile(t *testing.T, fn func(ctx context.Context, path string, data []byte, opts ...filesafe.WriteOption) (filesafe.Result, error)) {
	t.Helper()
	old := writeFile
	writeFile = fn
	t.Cleanup(func() { writeFile = old })
}

// notDurable makes the writeFile seam answer Durable false, after its write
// reached the path, while the flag it returns is true.
func notDurable(t *testing.T) *bool {
	t.Helper()
	on := true
	stubWriteFile(t, func(ctx context.Context, path string, data []byte, opts ...filesafe.WriteOption) (filesafe.Result, error) {
		res, err := filesafe.WriteFile(ctx, path, data, opts...)
		if on && err == nil {
			res.Durable = false
		}
		return res, err
	})
	return &on
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
	checkErr(t, err, name, is, isNot)
}

// checkErr checks that err matches each of is and none of isNot, names
// name and holds no hex run a key line's id or key would leave.
func checkErr(t *testing.T, err error, name string, is, isNot []error) {
	t.Helper()
	if err == nil {
		t.Fatal("nil error")
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
// KeyFile holding no key"): no directory, no EnsurePrivateDir call, no lock
// file and no lease, also for a key file or directory removed after
// OpenKeyFile found the file, before its directory check, after it, or
// before its read; and a done ctx does not fail it, since only a key file
// needs the lease.
func TestOpenKeyFileMissing(t *testing.T) {
	text := keyFileText(t, "one key")
	var made int
	stubEnsurePrivateDir(t, func(dir string, opts ...filesafe.PrivateDirOption) (filesafe.PrivateDirResult, error) {
		made++
		return filesafe.EnsurePrivateDir(dir, opts...)
	})
	open := func(t *testing.T, ctx context.Context, path string) (*KeyFile, error) {
		t.Helper()
		before := made
		f, err := OpenKeyFile(ctx, path)
		if made != before {
			t.Errorf("OpenKeyFile called EnsurePrivateDir %d times", made-before)
		}
		return f, err
	}
	t.Run("no directory", func(t *testing.T) {
		dir := filepath.Join(privateDir(t), "keys")
		f, err := open(t, t.Context(), filepath.Join(dir, "sealing.key"))
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
			f, err := open(t, c, filepath.Join(dir, "sealing.key"))
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
		f, err := open(t, t.Context(), path)
		checkKeyless(t, f, err)
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		if err := openKeyless(t, path).Create(ctx); err != nil {
			t.Errorf("a second KeyFile's Create = %v; want the lease free", err)
		}
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
			f, err := open(t, t.Context(), path)
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
		if want := fmt.Sprintf("owned by uid %d", uint32(os.Geteuid())); err != nil && !strings.Contains(err.Error(), want) {
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

// statInfo is a FileInfo whose Sys is st, the fstat(2) answer ownerModeErr
// reads.
type statInfo struct {
	fs.FileInfo
	st *syscall.Stat_t
}

func (i statInfo) Sys() any { return i.st }

// TestOwnerUIDs checks that the owner check compares and names uids as
// uid_t, an unsigned 32-bit value (stat(2) st_uid, inode(7)), for uids of
// 2^31 and over. The geteuid seam answers what os.Geteuid's int holds for
// them on a 32-bit build, where they arrive negative, so the check holds on
// every GOARCH.
func TestOwnerUIDs(t *testing.T) {
	const wrapped2p31, wrappedMax = -1 << 31, -2 // uids 2147483648 and 4294967294
	for _, tc := range []struct {
		name string
		uid  uint32
		euid int
		says string
	}{
		{"same uid 2147483648", 1 << 31, wrapped2p31, ""},
		{"owner 4294967294", 4294967294, wrapped2p31, "owned by uid 4294967294, not the effective uid 2147483648"},
		{"effective uid 4294967294", 1 << 31, wrappedMax, "owned by uid 2147483648, not the effective uid 4294967294"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubGeteuid(t, tc.euid)
			err := ownerModeErr("sealing.key", statInfo{st: &syscall.Stat_t{Uid: tc.uid, Mode: 0o600}})
			switch {
			case tc.says == "" && err != nil:
				t.Errorf("ownerModeErr = %v, want nil for the effective uid's file", err)
			case tc.says != "" && (!errors.Is(err, ErrUnsafeKeyFile) || !strings.Contains(fmt.Sprint(err), tc.says)):
				t.Errorf("ownerModeErr = %v, want ErrUnsafeKeyFile saying %q", err, tc.says)
			}
		})
	}
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
// OpenKeyFile to Close (the KeyFile and Close godoc; flock(2): a lock
// belongs to an open file description): a second OpenKeyFile here answers
// ErrKeyFileInUse, filesafe.ErrLeaseHeld and ctx's error at its deadline,
// or takes the lease within one 50 ms poll of Close; once closed, the
// Sealer holds no key, the writes answer fs.ErrClosed, a second Close nil,
// and the lock file stays. A done ctx is its error, not ErrKeyFileInUse.
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
			checkClosedWrites(t, a)
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

// checkClosedWrites checks that each of f's writes answers fs.ErrClosed, as
// the Close godoc says of a closed KeyFile, and Create makes no directory.
func checkClosedWrites(t *testing.T, f *KeyFile) {
	t.Helper()
	stubEnsurePrivateDir(t, func(dir string, _ ...filesafe.PrivateDirOption) (filesafe.PrivateDirResult, error) {
		t.Errorf("a closed KeyFile's Create called EnsurePrivateDir(%s)", dir)
		return filesafe.PrivateDirResult{}, nil
	})
	for name, write := range map[string]func(context.Context) error{"Create": f.Create, "Rotate": f.Rotate, "Retire": f.Retire} {
		if err := write(t.Context()); !errors.Is(err, fs.ErrClosed) {
			t.Errorf("%s after Close = %v, want fs.ErrClosed", name, err)
		}
	}
}

// TestLockFileRefused checks that each unsafe thing at <path>.lock an
// unprivileged test can make is, from OpenKeyFile and a keyless Create,
// ErrUnsafeKeyFile wrapping filesafe's AcquireLease refusal (its godoc: "a
// symlink (ErrSymlinkTarget), a non-regular file (ErrNotRegular) ... group
// or other access (ErrModeTooOpen)"), naming the lock file, before the key
// file is read (it is no key file) or written. A FIFO does not block the
// open (open(2) O_NONBLOCK), and a wide mode is left as found.
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
			// A socket path holds at most 107 bytes (unix(7) sun_path), which
			// a temporary directory's can pass: bind it from its directory.
			t.Chdir(filepath.Dir(lockPath))
			l, err := net.Listen("unix", filepath.Base(lockPath))
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
		checkMode := func(t *testing.T, lockPath string) {
			t.Helper()
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
		}
		t.Run("OpenKeyFile "+tc.name, func(t *testing.T) {
			path := writeKeyFile(t, privateDir(t), "not a key file\n", 0o600)
			lockPath := path + lockSuffix
			tc.make(t, lockPath)
			f, err := OpenKeyFile(t.Context(), path)
			checkRefusal(t, f, err, lockPath, []error{ErrUnsafeKeyFile, tc.is}, []error{ErrKeyFileFormat, ErrKeyFileInUse})
			checkMode(t, lockPath)
		})
		t.Run("Create "+tc.name, func(t *testing.T) {
			path := filepath.Join(privateDir(t), "sealing.key")
			lockPath := path + lockSuffix
			f := openKeyless(t, path)
			tc.make(t, lockPath)
			checkErr(t, f.Create(t.Context()), lockPath, []error{ErrUnsafeKeyFile, tc.is}, []error{ErrKeyExists, ErrKeyFileInUse})
			checkNoWrite(t, f, path)
			checkMode(t, lockPath)
		})
	}
}

// checkNoWrite checks that a keyless KeyFile's refused Create wrote no key
// file and holds no key and no lease.
func checkNoWrite(t *testing.T, f *KeyFile, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat(the key file) = %v; want none written", err)
	}
	if f.src.lease != nil {
		t.Error("the KeyFile holds the lease")
	}
	checkNoKey(t, f.Sealer())
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
// and a keyless Create answer each error filesafe's AcquireLease and
// ErrLeaseHeld godoc name: ErrLeaseHeld is ErrKeyFileInUse; each refusal
// ErrUnsafeKeyFile; a bare context error, ErrModeNotStored, ENOLCK, ErrRaced
// and fs.ErrNotExist are themselves, the last a missing key file to
// OpenKeyFile once the key file is gone too; a wait Close ended is
// fs.ErrClosed (context.Cause). Each call gets a context of the caller's
// and path+".lock", and each error names the file.
func TestLeaseAnswers(t *testing.T) {
	text := keyFileText(t, "one key")
	cases := []struct {
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
		{"lock path missing", func(p string) error {
			return &fs.PathError{Op: "open", Path: p, Err: syscall.ENOENT}
		}, []error{fs.ErrNotExist}, []error{ErrUnsafeKeyFile, ErrKeyFileInUse}},
		{"lock file replaced until ctx ended", func(p string) error {
			return fmt.Errorf("%w: %w", fmt.Errorf("%w: %s", filesafe.ErrRaced, p), context.DeadlineExceeded)
		}, []error{filesafe.ErrRaced, context.DeadlineExceeded}, []error{ErrKeyFileInUse, ErrUnsafeKeyFile}},
	}
	stub := func(t *testing.T, lockPath string, answer func(string) error) {
		stubAcquireLease(t, func(ctx context.Context, got string) (*filesafe.Lease, error) {
			if got != lockPath {
				t.Errorf("acquireLease(%q), want %q", got, lockPath)
			}
			if ctx.Value(leaseCtxKey{}) == nil {
				t.Error("acquireLease was not handed a context of the caller's")
			}
			return nil, answer(got)
		})
	}
	callerCtx := func(t *testing.T) context.Context { return context.WithValue(t.Context(), leaseCtxKey{}, true) }
	for _, tc := range cases {
		t.Run("OpenKeyFile "+tc.name, func(t *testing.T) {
			path := writeKeyFile(t, privateDir(t), text, 0o600)
			stub(t, path+lockSuffix, tc.answer)
			f, err := OpenKeyFile(callerCtx(t), path)
			checkRefusal(t, f, err, path+lockSuffix, tc.is, tc.isNot)
		})
		t.Run("Create "+tc.name, func(t *testing.T) {
			path := filepath.Join(privateDir(t), "sealing.key")
			f := openKeyless(t, path)
			stub(t, path+lockSuffix, tc.answer)
			checkErr(t, f.Create(callerCtx(t)), path+lockSuffix, tc.is, append([]error{fs.ErrClosed}, tc.isNot...))
			checkNoWrite(t, f, path)
		})
	}
	t.Run("OpenKeyFile lock path missing, key file gone", func(t *testing.T) {
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
	closeEnded := []struct {
		name   string
		answer func(ctx context.Context, lockPath string) error
	}{
		{"bare context error", func(ctx context.Context, _ string) error { return ctx.Err() }},
		{"lease held", func(ctx context.Context, p string) error {
			return fmt.Errorf("%w: %s: %w", filesafe.ErrLeaseHeld, p, ctx.Err())
		}},
		{"not regular", func(_ context.Context, p string) error {
			return &filesafe.NotRegularError{Name: p, Mode: fs.ModeDir | 0o700}
		}},
		{"lock file replaced", func(ctx context.Context, p string) error {
			return fmt.Errorf("%w: %w", fmt.Errorf("%w: %s", filesafe.ErrRaced, p), ctx.Err())
		}},
	}
	for _, tc := range closeEnded {
		t.Run("Create, Close ends the wait, "+tc.name, func(t *testing.T) {
			path := filepath.Join(privateDir(t), "sealing.key")
			f := openKeyless(t, path)
			closed := make(chan error, 1)
			stubAcquireLease(t, func(ctx context.Context, lockPath string) (*filesafe.Lease, error) {
				go func() { closed <- f.Close() }()
				select {
				case <-ctx.Done():
				case <-time.After(5 * time.Second):
					t.Error("Close did not end the lease wait")
				}
				return nil, tc.answer(ctx, lockPath)
			})
			checkErr(t, f.Create(t.Context()), path, []error{fs.ErrClosed}, []error{ErrKeyFileInUse, ErrUnsafeKeyFile})
			if err := <-closed; err != nil {
				t.Errorf("Close = %v, want nil", err)
			}
			checkNoWrite(t, f, path)
		})
	}
	t.Run("Create, its own deadline ends the wait", func(t *testing.T) {
		path := filepath.Join(privateDir(t), "sealing.key")
		f := openKeyless(t, path)
		stubAcquireLease(t, func(ctx context.Context, lockPath string) (*filesafe.Lease, error) {
			<-ctx.Done()
			return nil, fmt.Errorf("%w: %s: %w", filesafe.ErrLeaseHeld, lockPath, ctx.Err())
		})
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		checkErr(t, f.Create(ctx), path+lockSuffix, []error{ErrKeyFileInUse, context.DeadlineExceeded}, []error{fs.ErrClosed})
		checkNoWrite(t, f, path)
	})
}
