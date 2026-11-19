//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package sealed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/filesafe"
)

// pathState is what is at a path: its bytes, or its link target, and its
// identity (filesafe.Identify of its Lstat).
type pathState struct {
	id     filesafe.FileIdentity
	data   string
	absent bool
}

func statePath(t *testing.T, path string) pathState {
	t.Helper()
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return pathState{absent: true}
	}
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	s := pathState{id: filesafe.Identify(fi)}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		s.data, err = os.Readlink(path)
	case fi.Mode().IsRegular():
		var b []byte
		b, err = os.ReadFile(path)
		s.data = string(b)
	}
	if err != nil {
		t.Fatalf("read the path: %v", err)
	}
	return s
}

// checkUnchanged checks that path holds what before recorded, the same file
// included.
func checkUnchanged(t *testing.T, path string, before pathState) {
	t.Helper()
	fi, err := os.Lstat(path)
	switch {
	case before.absent:
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Lstat = %v; want nothing at the path, as before", err)
		}
		return
	case err != nil:
		t.Fatalf("Lstat: %v", err)
	}
	if !before.id.Matches(fi) {
		t.Error("another file is at the path")
	}
	if after := statePath(t, path); after.data != before.data {
		t.Errorf("the path holds %q, want %q kept", after.data, before.data)
	}
}

// checkSeals checks that s seals under the key ids want names and opens
// what it sealed with current true.
func checkSeals(t *testing.T, s *Sealer, want KeyIDs) {
	t.Helper()
	if got := s.KeyIDs(); got != want {
		t.Errorf("KeyIDs = %+v, want %+v", got, want)
	}
	v, err := s.SealAtRest("example/writes", []byte("x"))
	if err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}
	if p, cur, err := s.OpenAtRest("example/writes", v); err != nil || !cur || string(p) != "x" {
		t.Errorf("OpenAtRest = %q, %v, %v; want x, true, nil", p, cur, err)
	}
}

// TestKeyFileWrites checks what each write leaves at the path, read with
// this test's own reading of docs/format.md's grammar: Create one current
// line; Rotate a new current key, then the old one as previous; Retire the
// current key alone. Each file is at 0600 by the open descriptor's fstat
// (filesafe's WithMode: the published file holds the mode), each id is the
// RFC 5869 §2.3 transcription's, and the Sealer's KeyIDs name them.
func TestKeyFileWrites(t *testing.T) {
	path := filepath.Join(privateDir(t), "sealing.key")
	f := openKeyless(t, path)
	s := f.Sealer()
	written := func(t *testing.T, words ...string) []keyLine {
		t.Helper()
		lines := readKeyLines(t, path)
		if len(lines) != len(words) {
			t.Fatalf("the key file holds %d keys, want %d", len(lines), len(words))
		}
		for i, w := range words {
			if lines[i].word != w {
				t.Errorf("key line %d is %s, want %s", i+1, lines[i].word, w)
			}
		}
		kf, err := os.Open(path)
		if err != nil {
			t.Fatalf("open the key file: %v", err)
		}
		defer func() { _ = kf.Close() }()
		fi, err := kf.Stat()
		if err != nil {
			t.Fatalf("fstat: %v", err)
		}
		if got := fi.Mode(); got != 0o600 {
			t.Errorf("the key file's mode = %v, want -rw-------", got)
		}
		if got, want := s.KeyIDs(), lineIDs(lines); got != want {
			t.Errorf("KeyIDs = %+v, want the file's %+v", got, want)
		}
		return lines
	}
	if err := f.Create(t.Context()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	created := written(t, "current")
	if err := f.Rotate(t.Context()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	rotated := written(t, "current", "previous")
	if rotated[1].key != created[0].key {
		t.Error("Rotate did not keep the old current key as previous")
	}
	if rotated[0].key == created[0].key {
		t.Error("Rotate kept the old key current")
	}
	if err := f.Retire(t.Context()); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if retired := written(t, "current"); retired[0].key != rotated[0].key {
		t.Error("Retire did not keep the current key")
	}
}

// TestCreate checks Create against its godoc: it makes a missing directory,
// one level only, at 0700 and the key file at 0600; a Sealer taken before
// it seals after it (the Sealer godoc: it reads the keys at each call); a
// second Create is ErrKeyExists; and with anything at the path, Create is
// ErrKeyExists, leaves the path's bytes and identity (filesafe.Identify) as
// they were, and holds no key and no lease.
func TestCreate(t *testing.T) {
	t.Run("missing directory", func(t *testing.T) {
		dir := filepath.Join(privateDir(t), "keys")
		path := filepath.Join(dir, "sealing.key")
		f := openKeyless(t, path)
		s := f.Sealer()
		if err := f.Create(t.Context()); err != nil {
			t.Fatalf("Create: %v", err)
		}
		for name, want := range map[string]fs.FileMode{dir: fs.ModeDir | 0o700, path: 0o600} {
			if fi, err := os.Lstat(name); err != nil || fi.Mode() != want {
				t.Errorf("Lstat(%s) = %v, %v; want mode %v", name, fi.Mode(), err, want)
			}
		}
		checkSeals(t, s, lineIDs(readKeyLines(t, path)))
		if err := f.Create(t.Context()); !errors.Is(err, ErrKeyExists) {
			t.Errorf("second Create = %v, want ErrKeyExists", err)
		}
		checkInUse(t, path)
	})
	t.Run("two levels missing", func(t *testing.T) {
		parent := filepath.Join(privateDir(t), "a")
		path := filepath.Join(parent, "keys", "sealing.key")
		f := openKeyless(t, path)
		checkErr(t, f.Create(t.Context()), filepath.Join(parent, "keys"), []error{fs.ErrNotExist}, []error{ErrKeyExists, ErrUnsafeKeyFile})
		if _, err := os.Lstat(parent); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Lstat(the parent) = %v; want none made", err)
		}
		if f.src.lease != nil {
			t.Error("the KeyFile holds the lease")
		}
	})
	text := keyFileText(t, "one key")
	for _, tc := range []struct {
		name  string
		place func(t *testing.T, path string)
	}{
		{"key file", func(t *testing.T, path string) { writeKeyFile(t, filepath.Dir(path), text, 0o600) }},
		{"empty file", func(t *testing.T, path string) { writeKeyFile(t, filepath.Dir(path), "", 0o600) }},
		{"directory", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "elsewhere.key")
			if err := os.WriteFile(target, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run("a "+tc.name+" at the path", func(t *testing.T) {
			path := filepath.Join(privateDir(t), "sealing.key")
			f := openKeyless(t, path)
			tc.place(t, path)
			before := statePath(t, path)
			if err := f.Create(t.Context()); !errors.Is(err, ErrKeyExists) {
				t.Errorf("Create = %v, want ErrKeyExists", err)
			}
			checkUnchanged(t, path, before)
			if f.src.lease != nil {
				t.Error("the KeyFile holds the lease")
			}
			checkNoLease(t, path)
			checkNoKey(t, f.Sealer())
		})
	}
}

// TestCreateConcurrent checks that one KeyFile holds a key file at a time
// (the KeyFile godoc; flock(2): an exclusive lock excludes every other open
// file description): of eight keyless KeyFiles whose Creates run at once
// with 200 ms contexts, one answers nil and seven ErrKeyFileInUse at their
// deadline, and the file holds the winner's key. After the winner's Close a
// loser's Create, and one that waited while another holder wrote, is
// ErrKeyExists, writes nothing and holds no lease.
func TestCreateConcurrent(t *testing.T) {
	t.Run("a Create waiting while another holder writes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			path := filepath.Join(privateDir(t), "sealing.key")
			waiter := openKeyless(t, path)
			holder, err := filesafe.TryLease(path + lockSuffix)
			if err != nil {
				t.Fatalf("TryLease: %v", err)
			}
			created := make(chan error, 1)
			go func() { created <- waiter.Create(t.Context()) }()
			synctest.Wait()
			if _, err := filesafe.WriteFile(t.Context(), path, []byte(keyFileText(t, "one key")), filesafe.WithMode(0o600)); err != nil {
				t.Fatalf("the holder's write: %v", err)
			}
			before := statePath(t, path)
			if err := holder.Close(); err != nil {
				t.Fatalf("release the holder's lease: %v", err)
			}
			if err := <-created; !errors.Is(err, ErrKeyExists) {
				t.Errorf("the waiting Create = %v, want ErrKeyExists", err)
			}
			checkUnchanged(t, path, before)
			if waiter.src.lease != nil {
				t.Error("the waiting KeyFile holds the lease after ErrKeyExists")
			}
		})
	})
	path := filepath.Join(privateDir(t), "sealing.key")
	files := make([]*KeyFile, 8)
	for i := range files {
		files[i] = openKeyless(t, path)
	}
	errs := make([]error, len(files))
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			errs[i] = f.Create(ctx)
		})
	}
	wg.Wait()
	winner := -1
	for i, err := range errs {
		switch {
		case err == nil && winner >= 0:
			t.Errorf("Creates %d and %d both answered nil", winner, i)
		case err == nil:
			winner = i
		case !errors.Is(err, ErrKeyFileInUse) || !errors.Is(err, context.DeadlineExceeded):
			t.Errorf("Create %d = %v, want ErrKeyFileInUse and context.DeadlineExceeded", i, err)
		}
	}
	if winner < 0 {
		t.Fatal("no Create answered nil")
	}
	if got, want := lineIDs(readKeyLines(t, path)).Current, files[winner].Sealer().KeyIDs().Current; got != want {
		t.Errorf("the key file's id = %s, want the winner's %s", got, want)
	}
	if err := files[winner].Close(); err != nil {
		t.Fatalf("Close the winner: %v", err)
	}
	loser := files[(winner+1)%len(files)]
	before := statePath(t, path)
	if err := loser.Create(t.Context()); !errors.Is(err, ErrKeyExists) {
		t.Errorf("a loser's Create after the winner's Close = %v, want ErrKeyExists", err)
	}
	checkUnchanged(t, path, before)
	if loser.src.lease != nil {
		t.Error("the loser holds the lease after ErrKeyExists")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	f, err := OpenKeyFile(ctx, path)
	if err != nil {
		t.Fatalf("OpenKeyFile while the loser stays open: %v", err)
	}
	_ = f.Close()
}

// TestRotateRetire checks the rotation rules of the Rotate and Retire godoc,
// which Tink's keyset.Manager shares (the primary key is never removed): a
// second Rotate before Retire is ErrRotationPending and writes nothing;
// Retire with no previous key answers nil and leaves the file as it was,
// the same file included; both answer ErrNoKey on a keyless KeyFile.
func TestRotateRetire(t *testing.T) {
	path := filepath.Join(privateDir(t), "sealing.key")
	t.Run("keyless", func(t *testing.T) {
		f := openKeyless(t, path)
		for name, write := range map[string]func(context.Context) error{"Rotate": f.Rotate, "Retire": f.Retire} {
			if err := write(t.Context()); !errors.Is(err, ErrNoKey) {
				t.Errorf("%s = %v, want ErrNoKey", name, err)
			}
		}
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Lstat = %v; want nothing written", err)
		}
	})
	f := createKeyFile(t, path)
	if err := f.Rotate(t.Context()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	ids := f.Sealer().KeyIDs()
	before := statePath(t, path)
	if err := f.Rotate(t.Context()); !errors.Is(err, ErrRotationPending) {
		t.Errorf("second Rotate = %v, want ErrRotationPending", err)
	}
	checkUnchanged(t, path, before)
	if got := f.Sealer().KeyIDs(); got != ids {
		t.Errorf("KeyIDs after the refused Rotate = %+v, want %+v", got, ids)
	}
	if err := f.Retire(t.Context()); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	retired := statePath(t, path)
	if err := f.Retire(t.Context()); err != nil {
		t.Errorf("second Retire = %v, want nil", err)
	}
	checkUnchanged(t, path, retired)
	checkSeals(t, f.Sealer(), KeyIDs{Current: ids.Current})
}

// TestRingAcrossRotation checks a key file's ring the way Tink's keysets
// define one ("Keysets": the primary key encrypts, and decryption tries
// every key): after Rotate a value of the old key opens with current false
// and, sealed again, with current true; after Retire the old key's values
// are ErrInvalid and the re-sealed one still opens.
func TestRingAcrossRotation(t *testing.T) {
	const purpose Purpose = "example/rotation"
	f := createKeyFile(t, filepath.Join(privateDir(t), "sealing.key"))
	s := f.Sealer()
	vals := map[string]string{}
	for _, k := range kinds {
		vals[k.name] = mustSeal(t, k, s, purpose, []byte(k.name))
	}
	if err := f.Rotate(t.Context()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	resealed := map[string]string{}
	for _, k := range kinds {
		p, cur, err := k.open(s)(purpose, vals[k.name])
		if err != nil || cur || string(p) != k.name {
			t.Errorf("%s: the old key's value after Rotate = %q, %v, %v; want %s, false, nil", k.name, p, cur, err, k.name)
		}
		resealed[k.name] = mustSeal(t, k, s, purpose, p)
		if _, cur, err := k.open(s)(purpose, resealed[k.name]); err != nil || !cur {
			t.Errorf("%s: the value sealed again = %v, %v; want current", k.name, cur, err)
		}
	}
	if err := f.Retire(t.Context()); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	for _, k := range kinds {
		if p, cur, err := k.open(s)(purpose, vals[k.name]); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: the old key's value after Retire = %q, %v, %v; want ErrInvalid", k.name, p, cur, err)
		}
		if p, cur, err := k.open(s)(purpose, resealed[k.name]); err != nil || !cur || string(p) != k.name {
			t.Errorf("%s: the value sealed again, after Retire = %q, %v, %v", k.name, p, cur, err)
		}
	}
}

// TestMACAcrossRotation checks VerifyMAC on a key file across a rotation
// (the VerifyMAC godoc; RFC 2104 §2 tags compared as crypto/hmac.Equal's
// docs say): after Rotate the old key's (id, tag) verifies with current
// false and MAC answers the new key's id; after Retire it is ErrInvalid.
func TestMACAcrossRotation(t *testing.T) {
	const label Label = "example/manifest"
	msg := []byte("a manifest")
	f := createKeyFile(t, filepath.Join(privateDir(t), "sealing.key"))
	s := f.Sealer()
	id, tag, err := s.MAC(label, msg)
	if err != nil {
		t.Fatalf("MAC: %v", err)
	}
	if err := f.Rotate(t.Context()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if cur, err := s.VerifyMAC(label, msg, id, tag); err != nil || cur {
		t.Errorf("VerifyMAC(the old key's tag) after Rotate = %v, %v; want false, nil", cur, err)
	}
	if newID, _, err := s.MAC(label, msg); err != nil || newID != s.KeyIDs().Current || newID == id {
		t.Errorf("MAC after Rotate = id %s, %v; want the new current key's %s", newID, err, s.KeyIDs().Current)
	}
	if err := f.Retire(t.Context()); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if cur, err := s.VerifyMAC(label, msg, id, tag); !errors.Is(err, ErrInvalid) || cur {
		t.Errorf("VerifyMAC(the old key's tag) after Retire = %v, %v; want false, ErrInvalid", cur, err)
	}
}

// TestSubkeyAcrossRotation checks the Subkey godoc on a key file: the same
// key after a restart (the KeyFile closed, the path opened again), equal
// to the RFC 5869 §2 transcription under the file's current key, and a new
// one after Rotate. The copy taken before keeps its key through Rotate and
// Retire: a New Sealer over it opens what it sealed, never with current
// false, and one over the new Subkey refuses it (RFC 5116 §2.2: FAIL
// unless the key matches).
func TestSubkeyAcrossRotation(t *testing.T) {
	const label Label = "example/cursor"
	path := filepath.Join(privateDir(t), "sealing.key")
	a := createKeyFile(t, path)
	before, err := a.Sealer().Subkey(label)
	if err != nil {
		t.Fatalf("Subkey: %v", err)
	}
	if want := refHKDF(readKeyLines(t, path)[0].key[:], nil, "sealed/subkey\x00"+string(label), keyLen); !bytes.Equal(before[:], want) {
		t.Errorf("Subkey = %x, want the current key's %x", before, want)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	b := openKeyFile(t, path)
	if again, err := b.Sealer().Subkey(label); err != nil || again != before {
		t.Errorf("Subkey after a restart = %x, %v; want %x", again, err, before)
	}
	value, err := New(before).SealAtRest("example/cursor", []byte("c"))
	if err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}
	if err := b.Rotate(t.Context()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	after, err := b.Sealer().Subkey(label)
	if err != nil || after == before {
		t.Fatalf("Subkey after Rotate = %x, %v; want a new key", after, err)
	}
	if want := refHKDF(readKeyLines(t, path)[0].key[:], nil, "sealed/subkey\x00"+string(label), keyLen); !bytes.Equal(after[:], want) {
		t.Errorf("Subkey after Rotate = %x, want the new current key's %x", after, want)
	}
	if err := b.Retire(t.Context()); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if p, cur, err := New(before).OpenAtRest("example/cursor", value); err != nil || !cur || string(p) != "c" {
		t.Errorf("New(the old Subkey).OpenAtRest after Retire = %q, %v, %v; want c, true, nil", p, cur, err)
	}
	if _, _, err := New(after).OpenAtRest("example/cursor", value); !errors.Is(err, ErrInvalid) {
		t.Errorf("New(the new Subkey).OpenAtRest(the old value) = %v, want ErrInvalid", err)
	}
}

// TestKeyFileChanged checks that Rotate and Retire never write over a file
// they did not leave (the ErrKeyFileChanged godoc): with another key file
// at the path, no file, or a lock file other than the one A's lease locked
// (flock(2) locks an open file, so a new one takes a second lease), both
// answer ErrKeyFileChanged, before ErrRotationPending or Retire's nil, the
// path stays as it was, and A keeps sealing under its keys. A lease check
// whose lookup fails (EIO) answers that error and writes nothing; a later
// Rotate writes.
func TestKeyFileChanged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, path string)
	}{
		{"another key file", func(t *testing.T, path string) {
			if _, err := filesafe.WriteFile(t.Context(), path, []byte(keyFileText(t, "two keys")), filesafe.WithMode(0o600)); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
		}},
		{"no key file", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{"another lock file", func(t *testing.T, path string) {
			if err := os.Remove(path + lockSuffix); err != nil {
				t.Fatal(err)
			}
			openKeyFile(t, path)
		}},
	} {
		for _, start := range []struct {
			name   string
			rotate bool
		}{{"one key", false}, {"two keys", true}} {
			t.Run(tc.name+" with "+start.name, func(t *testing.T) {
				path := filepath.Join(privateDir(t), "sealing.key")
				a := createKeyFile(t, path)
				if start.rotate {
					if err := a.Rotate(t.Context()); err != nil {
						t.Fatalf("Rotate before the change: %v", err)
					}
				}
				ids := a.Sealer().KeyIDs()
				tc.change(t, path)
				before := statePath(t, path)
				for name, write := range map[string]func(context.Context) error{"Rotate": a.Rotate, "Retire": a.Retire} {
					if err := write(t.Context()); !errors.Is(err, ErrKeyFileChanged) {
						t.Errorf("%s = %v, want ErrKeyFileChanged", name, err)
					}
				}
				checkUnchanged(t, path, before)
				checkSeals(t, a.Sealer(), ids)
			})
		}
	}
	t.Run("lease check fails", func(t *testing.T) {
		path := filepath.Join(privateDir(t), "sealing.key")
		a := createKeyFile(t, path)
		failing := true
		stubLeaseCovers(t, func(l *filesafe.Lease) (bool, error) {
			if failing {
				return false, &fs.PathError{Op: "lstat", Path: path + lockSuffix, Err: syscall.EIO}
			}
			return l.Covers()
		})
		before := statePath(t, path)
		checkErr(t, a.Rotate(t.Context()), path+lockSuffix, []error{syscall.EIO}, []error{ErrKeyFileChanged})
		checkUnchanged(t, path, before)
		failing = false
		if err := a.Rotate(t.Context()); err != nil {
			t.Fatalf("Rotate with the lookup healthy: %v", err)
		}
		if got := len(readKeyLines(t, path)); got != 2 {
			t.Errorf("the key file holds %d keys after Rotate, want 2", got)
		}
	})
}

// TestNotDurable checks the ErrNotDurable godoc through the writeFile seam,
// whose write reaches the path and answers Durable false (filesafe's Result
// docs: "False means the data is present at Path but may not survive";
// docs/durability.md: a caller treats it "as a failure to act on, by
// retrying or alerting"): nothing is installed, the lease is kept so no
// other KeyFile opens the file, a retry writes over it, and only that file:
// any other at the path is ErrKeyExists.
func TestNotDurable(t *testing.T) {
	t.Run("Create", func(t *testing.T) {
		path := filepath.Join(privateDir(t), "sealing.key")
		f := openKeyless(t, path)
		on := notDurable(t)
		for _, try := range []string{"first", "retry"} {
			if err := f.Create(t.Context()); !errors.Is(err, ErrNotDurable) {
				t.Fatalf("%s Create = %v, want ErrNotDurable", try, err)
			}
			if got := len(readKeyLines(t, path)); got != 1 {
				t.Errorf("%s: the path holds %d keys, want the new key file's 1", try, got)
			}
			checkNoKey(t, f.Sealer())
			checkInUse(t, path)
		}
		*on = false
		if err := f.Create(t.Context()); err != nil {
			t.Fatalf("Create once durable = %v, want nil over its own write", err)
		}
		checkSeals(t, f.Sealer(), lineIDs(readKeyLines(t, path)))
	})
	t.Run("Rotate", func(t *testing.T) {
		path := filepath.Join(privateDir(t), "sealing.key")
		f := createKeyFile(t, path)
		ids := f.Sealer().KeyIDs()
		old := mustSeal(t, kinds[1], f.Sealer(), "example/durable", []byte("x"))
		on := notDurable(t)
		var uninstalled KeyIDs
		for _, try := range []string{"first", "retry"} {
			if err := f.Rotate(t.Context()); !errors.Is(err, ErrNotDurable) {
				t.Fatalf("%s Rotate = %v, want ErrNotDurable", try, err)
			}
			uninstalled = lineIDs(readKeyLines(t, path))
			if uninstalled.Previous != ids.Current || uninstalled.Current == ids.Current {
				t.Errorf("%s: the path holds %+v, want a new key over %s", try, uninstalled, ids.Current)
			}
			checkSeals(t, f.Sealer(), ids)
			if _, cur, err := f.Sealer().OpenAtRest("example/durable", old); err != nil || !cur {
				t.Errorf("%s: the old value = %v, %v; want it current", try, cur, err)
			}
		}
		before := statePath(t, path)
		if err := f.Create(t.Context()); !errors.Is(err, ErrKeyExists) {
			t.Errorf("Create on the KeyFile holding a key = %v, want ErrKeyExists over its uninstalled write too", err)
		}
		checkUnchanged(t, path, before)
		*on = false
		if err := f.Rotate(t.Context()); err != nil {
			t.Fatalf("Rotate once durable: %v", err)
		}
		got := f.Sealer().KeyIDs()
		if got.Previous != ids.Current || got.Current == uninstalled.Current || got != lineIDs(readKeyLines(t, path)) {
			t.Errorf("KeyIDs after the retry = %+v; want a new current key over %s, not the uninstalled %s", got, ids.Current, uninstalled.Current)
		}
	})
	t.Run("Retire", func(t *testing.T) {
		path := filepath.Join(privateDir(t), "sealing.key")
		f := createKeyFile(t, path)
		old := mustSeal(t, kinds[1], f.Sealer(), "example/durable", []byte("x"))
		if err := f.Rotate(t.Context()); err != nil {
			t.Fatalf("Rotate: %v", err)
		}
		ids := f.Sealer().KeyIDs()
		on := notDurable(t)
		if err := f.Retire(t.Context()); !errors.Is(err, ErrNotDurable) {
			t.Fatalf("Retire = %v, want ErrNotDurable", err)
		}
		if got := lineIDs(readKeyLines(t, path)); got != (KeyIDs{Current: ids.Current}) {
			t.Errorf("the path holds %+v, want the current key alone", got)
		}
		if got := f.Sealer().KeyIDs(); got != ids {
			t.Errorf("KeyIDs = %+v, want %+v kept", got, ids)
		}
		if _, cur, err := f.Sealer().OpenAtRest("example/durable", old); err != nil || cur {
			t.Errorf("the previous key's value = %v, %v; want it open, not current", cur, err)
		}
		*on = false
		if err := f.Retire(t.Context()); err != nil {
			t.Fatalf("Retire once durable: %v", err)
		}
		checkSeals(t, f.Sealer(), KeyIDs{Current: ids.Current})
	})
	t.Run("another key file over the uninstalled write", func(t *testing.T) {
		path := filepath.Join(privateDir(t), "sealing.key")
		f := openKeyless(t, path)
		on := notDurable(t)
		if err := f.Create(t.Context()); !errors.Is(err, ErrNotDurable) {
			t.Fatalf("Create = %v, want ErrNotDurable", err)
		}
		*on = false
		if _, err := filesafe.WriteFile(t.Context(), path, []byte(keyFileText(t, "one key")), filesafe.WithMode(0o600)); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		before := statePath(t, path)
		if err := f.Create(t.Context()); !errors.Is(err, ErrKeyExists) {
			t.Errorf("Create = %v, want ErrKeyExists", err)
		}
		checkUnchanged(t, path, before)
		if f.src.lease != nil {
			t.Error("the KeyFile holds the lease after ErrKeyExists")
		}
		checkNoLease(t, path)
	})
	t.Run("adopted after Close", func(t *testing.T) {
		path := filepath.Join(privateDir(t), "sealing.key")
		f := openKeyless(t, path)
		notDurable(t)
		if err := f.Create(t.Context()); !errors.Is(err, ErrNotDurable) {
			t.Fatalf("Create = %v, want ErrNotDurable", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if got, want := openKeyFile(t, path).Sealer().KeyIDs(), lineIDs(readKeyLines(t, path)); got != want {
			t.Errorf("a new OpenKeyFile's KeyIDs = %+v, want the file's %+v", got, want)
		}
	})
}

// TestCloseDuringWrite checks the Close godoc: it waits for a key-file write
// already running, and the write cannot install its ring after Close; a
// Create waiting for another holder's lease stops at once, without one of
// AcquireLease's 50 ms polls passing on the synctest bubble's clock; and a
// call that has not begun its write, before or after its lease, answers
// fs.ErrClosed, writing nothing, never waiting for a lease, and holding
// none it took.
func TestCloseDuringWrite(t *testing.T) {
	t.Run("a write running", func(t *testing.T) {
		path := filepath.Join(privateDir(t), "sealing.key")
		f := createKeyFile(t, path)
		old := f.Sealer().KeyIDs().Current
		entered, release := make(chan struct{}), make(chan struct{})
		stubWriteFile(t, func(ctx context.Context, p string, data []byte, opts ...filesafe.WriteOption) (filesafe.Result, error) {
			close(entered)
			<-release
			return filesafe.WriteFile(ctx, p, data, opts...)
		})
		rotated := make(chan error, 1)
		go func() { rotated <- f.Rotate(t.Context()) }()
		<-entered
		closed := make(chan error, 1)
		go func() { closed <- f.Close() }()
		select {
		case err := <-closed:
			t.Errorf("Close returned while the write ran: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		close(release)
		if err := <-rotated; err != nil {
			t.Errorf("Rotate = %v, want nil", err)
		}
		if err := <-closed; err != nil {
			t.Errorf("Close = %v, want nil", err)
		}
		checkNoKey(t, f.Sealer())
		if got := lineIDs(readKeyLines(t, path)); got.Previous != old {
			t.Errorf("the path holds %+v, want the rotated file over %s", got, old)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		g, err := OpenKeyFile(ctx, path)
		if err != nil {
			t.Fatalf("OpenKeyFile after Close: %v", err)
		}
		_ = g.Close()
		checkClosedWrites(t, f)
	})
	t.Run("a lease wait", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			path := filepath.Join(privateDir(t), "sealing.key")
			waiter := openKeyless(t, path)
			createKeyFile(t, path)
			created := make(chan error, 1)
			go func() { created <- waiter.Create(context.Background()) }()
			synctest.Wait()
			start := time.Now()
			closed := make(chan error, 1)
			go func() { closed <- waiter.Close() }()
			checkErr(t, <-created, path, []error{fs.ErrClosed}, []error{ErrKeyFileInUse})
			if err := <-closed; err != nil {
				t.Errorf("Close = %v, want nil", err)
			}
			if waited := time.Since(start); waited >= 50*time.Millisecond {
				t.Errorf("Close and the Create took %v, one 50 ms poll or more", waited)
			}
			if waiter.src.lease != nil {
				t.Error("the closed KeyFile holds the lease")
			}
			checkInUse(t, path)
		})
	})
	t.Run("before the lease", func(t *testing.T) {
		path := filepath.Join(privateDir(t), "sealing.key")
		f := openKeyless(t, path)
		holder, err := filesafe.TryLease(path + lockSuffix)
		if err != nil {
			t.Fatalf("TryLease: %v", err)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		blocked := true
		stubEnsurePrivateDir(t, func(dir string, opts ...filesafe.PrivateDirOption) (filesafe.PrivateDirResult, error) {
			if blocked {
				blocked = false
				close(entered)
				<-release
			}
			return filesafe.EnsurePrivateDir(dir, opts...)
		})
		created := make(chan error, 1)
		go func() { created <- f.Create(t.Context()) }()
		<-entered
		closed := make(chan error, 1)
		go func() { closed <- f.Close() }()
		<-f.closing
		close(release)
		select {
		case err := <-created:
			checkErr(t, err, path, []error{fs.ErrClosed}, []error{ErrKeyFileInUse})
		case <-time.After(5 * time.Second):
			t.Error("the Create waits for the lease after Close began")
			_ = holder.Close()
			<-created
		}
		if err := <-closed; err != nil {
			t.Errorf("Close = %v, want nil", err)
		}
		checkNoWrite(t, f, path)
		_ = holder.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		if err := openKeyless(t, path).Create(ctx); err != nil {
			t.Errorf("a second KeyFile's Create = %v, want the lease free", err)
		}
	})
	t.Run("Create after the lease", func(t *testing.T) {
		path := filepath.Join(privateDir(t), "sealing.key")
		f := openKeyless(t, path)
		closed := make(chan error, 1)
		stubAcquireLease(t, func(ctx context.Context, lockPath string) (*filesafe.Lease, error) {
			l, err := filesafe.AcquireLease(ctx, lockPath)
			go func() { closed <- f.Close() }()
			<-f.closing
			return l, err
		})
		checkErr(t, f.Create(t.Context()), path, []error{fs.ErrClosed}, nil)
		if err := <-closed; err != nil {
			t.Errorf("Close = %v, want nil", err)
		}
		checkNoWrite(t, f, path)
		checkNoLease(t, path)
	})
	t.Run("Rotate after the lease check", func(t *testing.T) {
		path := filepath.Join(privateDir(t), "sealing.key")
		f := createKeyFile(t, path)
		entered, release := make(chan struct{}), make(chan struct{})
		stubLeaseCovers(t, func(l *filesafe.Lease) (bool, error) {
			close(entered)
			<-release
			return l.Covers()
		})
		before := statePath(t, path)
		rotated := make(chan error, 1)
		go func() { rotated <- f.Rotate(t.Context()) }()
		<-entered
		closed := make(chan error, 1)
		go func() { closed <- f.Close() }()
		<-f.closing
		close(release)
		checkErr(t, <-rotated, path, []error{fs.ErrClosed}, nil)
		if err := <-closed; err != nil {
			t.Errorf("Close = %v, want nil", err)
		}
		checkUnchanged(t, path, before)
		checkNoLease(t, path)
	})
}

// parentNotSynced is EnsurePrivateDir's answer when dir's parent could not
// be fsynced, dir left in place.
func parentNotSynced(dir string) error {
	return fmt.Errorf("%w: %s: %w", filesafe.ErrParentNotSynced, dir, syscall.EIO)
}

// parentUnreadable is EnsurePrivateDir's answer when dir's parent can be
// traversed but not read, so no sync can open it.
func parentUnreadable(dir string) error {
	parent := filepath.Dir(dir)
	return fmt.Errorf("filesafe: parent %s is not readable by the effective uid (%s left in place): %w",
		parent, dir, &fs.PathError{Op: "open", Path: parent, Err: syscall.EACCES})
}

// TestCreateDirNotDurable checks Create through the ensurePrivateDir seam
// against filesafe's PrivateDirResult and ErrParentNotSynced docs (a
// directory entry never synced into its parent "can vanish in a crash",
// docs/durability.md; a caller may retry, and the directory is left): a
// failed parent sync is ErrNotDurable, an unreadable parent fs.ErrPermission,
// and neither writes a key; a KeyFile holding its uninstalled write keeps
// the lease and the record while its lock file is the one at the path, or
// the check cannot tell, and drops both once it is not.
func TestCreateDirNotDurable(t *testing.T) {
	keyDir := func(t *testing.T) (dir, path string) {
		dir = filepath.Join(privateDir(t), "keys")
		return dir, filepath.Join(dir, "sealing.key")
	}
	for _, tc := range []struct {
		name   string
		found  bool
		answer func(dir string) error
		is     []error
		isNot  []error
	}{
		{
			"parent sync failed, directory made", false, parentNotSynced,
			[]error{ErrNotDurable, filesafe.ErrParentNotSynced},
			[]error{ErrKeyExists},
		},
		{
			"parent sync failed, directory found", true, parentNotSynced,
			[]error{ErrNotDurable, filesafe.ErrParentNotSynced},
			[]error{ErrKeyExists},
		},
		{
			"parent unreadable", false, parentUnreadable,
			[]error{fs.ErrPermission},
			[]error{ErrNotDurable, ErrUnsafeKeyFile, ErrKeyExists},
		},
	} {
		t.Run("from keyless, "+tc.name, func(t *testing.T) {
			dir, path := keyDir(t)
			if tc.found {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			f := openKeyless(t, path)
			failing := true
			stubEnsurePrivateDir(t, func(d string, opts ...filesafe.PrivateDirOption) (filesafe.PrivateDirResult, error) {
				if !failing {
					return filesafe.EnsurePrivateDir(d, opts...)
				}
				// filesafe leaves the directory, made or found, on both answers.
				if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
					t.Errorf("mkdir: %v", err)
				}
				return filesafe.PrivateDirResult{}, tc.answer(d)
			})
			checkErr(t, f.Create(t.Context()), dir, tc.is, tc.isNot)
			checkNoWrite(t, f, path)
			if _, err := os.Lstat(path + lockSuffix); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Lstat(the lock file) = %v; want no lease taken", err)
			}
			failing = false
			if err := f.Create(t.Context()); err != nil {
				t.Fatalf("Create once the directory is healthy: %v", err)
			}
		})
	}
	// written returns a KeyFile holding the record of a Create whose write
	// filesafe did not report durable.
	written := func(t *testing.T, path string) *KeyFile {
		t.Helper()
		f := openKeyless(t, path)
		on := notDurable(t)
		if err := f.Create(t.Context()); !errors.Is(err, ErrNotDurable) {
			t.Fatalf("Create = %v, want ErrNotDurable", err)
		}
		*on = false
		return f
	}
	for _, tc := range []struct {
		name   string
		answer func(dir string) error
		is     []error
	}{
		{"directory refused", func(dir string) error {
			return fmt.Errorf("%w: %s (mode 0750)", filesafe.ErrModeTooOpen, dir)
		}, []error{ErrUnsafeKeyFile, filesafe.ErrModeTooOpen}},
		{"parent sync failed", parentNotSynced, []error{ErrNotDurable, filesafe.ErrParentNotSynced}},
		{"parent unreadable", parentUnreadable, []error{fs.ErrPermission}},
	} {
		t.Run("from keyless, written, "+tc.name, func(t *testing.T) {
			dir, path := keyDir(t)
			f := written(t, path)
			failing := true
			stubEnsurePrivateDir(t, func(d string, opts ...filesafe.PrivateDirOption) (filesafe.PrivateDirResult, error) {
				if failing {
					return filesafe.PrivateDirResult{}, tc.answer(d)
				}
				return filesafe.EnsurePrivateDir(d, opts...)
			})
			checkErr(t, f.Create(t.Context()), dir, tc.is, []error{ErrKeyExists})
			checkInUse(t, path)
			failing = false
			if err := f.Create(t.Context()); err != nil {
				t.Errorf("Create with the directory healthy = %v, want nil over its own write", err)
			}
		})
	}
	t.Run("from keyless, written, directory removed, mkdir fails", func(t *testing.T) {
		dir, path := keyDir(t)
		f := written(t, path)
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		stubEnsurePrivateDir(t, func(d string, _ ...filesafe.PrivateDirOption) (filesafe.PrivateDirResult, error) {
			return filesafe.PrivateDirResult{}, &fs.PathError{Op: "mkdir", Path: d, Err: syscall.ENOSPC}
		})
		checkErr(t, f.Create(t.Context()), dir, []error{syscall.ENOSPC}, []error{ErrKeyExists, ErrNotDurable})
		if f.src.lease != nil || f.pending != (KeyIDs{}) {
			t.Errorf("the KeyFile holds lease %v and record %+v; want neither", f.src.lease != nil, f.pending)
		}
	})
	t.Run("from keyless, written, directory removed, then healthy", func(t *testing.T) {
		dir, path := keyDir(t)
		f := written(t, path)
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := f.Create(t.Context()); err != nil {
			t.Fatalf("Create = %v, want a new lease and key", err)
		}
		if covered, err := f.src.lease.Covers(); err != nil || !covered {
			t.Errorf("the new lease covers the path: %v, %v", covered, err)
		}
		checkSeals(t, f.Sealer(), lineIDs(readKeyLines(t, path)))
	})
	t.Run("from keyless, written, directory made again, parent sync fails", func(t *testing.T) {
		dir, path := keyDir(t)
		f := written(t, path)
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+lockSuffix, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		stubEnsurePrivateDir(t, func(d string, _ ...filesafe.PrivateDirOption) (filesafe.PrivateDirResult, error) {
			return filesafe.PrivateDirResult{}, parentNotSynced(d)
		})
		checkErr(t, f.Create(t.Context()), dir, []error{ErrNotDurable, filesafe.ErrParentNotSynced}, []error{ErrKeyExists})
		if f.src.lease != nil || f.pending != (KeyIDs{}) {
			t.Errorf("the KeyFile holds lease %v and record %+v; want neither", f.src.lease != nil, f.pending)
		}
		checkNoLease(t, path)
	})
	t.Run("from keyless, written, lease check fails", func(t *testing.T) {
		_, path := keyDir(t)
		f := written(t, path)
		failing := true
		stubLeaseCovers(t, func(l *filesafe.Lease) (bool, error) {
			if failing {
				return false, &fs.PathError{Op: "lstat", Path: path + lockSuffix, Err: syscall.EIO}
			}
			return l.Covers()
		})
		before := statePath(t, path)
		checkErr(t, f.Create(t.Context()), path+lockSuffix, []error{syscall.EIO}, []error{ErrKeyExists})
		checkUnchanged(t, path, before)
		checkInUse(t, path)
		failing = false
		if err := f.Create(t.Context()); err != nil {
			t.Errorf("Create with the lookup healthy = %v, want nil over its own write", err)
		}
	})
}

// leaseChildMode names what the helper process of TestLeaseAcrossProcesses
// does with the key file in its working directory.
const leaseChildMode = "SEALED_TEST_LEASE_MODE"

// TestLeaseAcrossProcesses checks the lease against another process, this
// test binary started again (flock(2): a lock belongs to an open file
// description, and an exclusive one excludes every other): while this
// process holds the key file, the child's OpenKeyFile answers
// ErrKeyFileInUse at its deadline; after Close a child opens the file,
// rotates and exits, and this process opens the rotated file.
func TestLeaseAcrossProcesses(t *testing.T) {
	if mode := os.Getenv(leaseChildMode); mode != "" {
		leaseChild(t, mode)
		return
	}
	path := filepath.Join(privateDir(t), "sealing.key")
	a := createKeyFile(t, path)
	ids := a.Sealer().KeyIDs()
	if out, err := runLeaseChild(t, path, "held"); err != nil {
		t.Fatalf("the child holding off: %v\n%s", err, out)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	out, err := runLeaseChild(t, path, "rotate")
	if err != nil {
		t.Fatalf("the child rotating: %v\n%s", err, out)
	}
	got := openKeyFile(t, path).Sealer().KeyIDs()
	if got.Previous != ids.Current || got.Current == ids.Current || !strings.Contains(out, "rotated "+got.Current) {
		t.Errorf("KeyIDs after the child's Rotate = %+v; want the child's new key over %s\n%s", got, ids.Current, out)
	}
}

func runLeaseChild(t *testing.T, path, mode string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestLeaseAcrossProcesses$", "-test.count=1")
	cmd.Dir = filepath.Dir(path)
	cmd.Env = append(os.Environ(), leaseChildMode+"="+mode)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// leaseChild is the helper process's side, on sealing.key in its working
// directory: "held" expects ErrKeyFileInUse at a 200 ms deadline, and
// "rotate" opens, rotates and prints the new id.
func leaseChild(t *testing.T, mode string) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	path := filepath.Join(wd, "sealing.key")
	switch mode {
	case "held":
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		f, err := OpenKeyFile(ctx, path)
		if f != nil {
			_ = f.Close()
		}
		if !errors.Is(err, ErrKeyFileInUse) || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("OpenKeyFile in a second process = %v, want ErrKeyFileInUse at its deadline", err)
		}
	case "rotate":
		f := openKeyFile(t, path)
		if err := f.Rotate(t.Context()); err != nil {
			t.Fatalf("Rotate in a second process: %v", err)
		}
		fmt.Println("rotated", f.Sealer().KeyIDs().Current)
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}
