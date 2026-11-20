package sealed

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/cplieger/go/filesafe"
)

// Root passes every permission check, so a root suite runs
// TestForeignKeyDir again in a copy of the test binary as nobody, with
// nobodyChild set, in the tree it built as the working directory. Linux
// only: an unprivileged suite uses /root, another uid's directory it
// cannot enter.
const (
	nobodyUID   = 65534
	nobodyChild = "SEALED_TEST_NOBODY_CHILD"
	nobodyReady = "sealed-test: waiting for the parent"
)

// TestForeignKeyDir checks the refusal of a key directory another user
// owns at 0700, which this user cannot enter (the filesafe.ErrNotOwned
// contract: a directory whose owner's mode refused the open is ErrNotOwned
// too, wrapping the EACCES): OpenKeyFile on a key file there and a keyless
// KeyFile's Create answer ErrUnsafeKeyFile matching filesafe.ErrNotOwned
// and fs.ErrPermission, holding no key and no lease. A key directory this
// user owns and cannot search stays a plain permission error.
func TestForeignKeyDir(t *testing.T) {
	child := os.Getenv(nobodyChild) != ""
	if !child && os.Geteuid() == 0 {
		runAsNobody(t)
		return
	}
	// The tree is the working directory: gosec's G703 taint analysis follows
	// a path read from the environment into the package's own os.Lstat.
	base, closed := "", "/root"
	if child {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd: %v", err)
		}
		base, closed = wd, filepath.Join(wd, "closed")
	} else {
		checkForeignClosed(t, closed)
	}
	refused := []error{ErrUnsafeKeyFile, filesafe.ErrNotOwned, fs.ErrPermission}
	t.Run("OpenKeyFile", func(t *testing.T) {
		path := filepath.Join(closed, "sealing.key")
		f, err := OpenKeyFile(t.Context(), path)
		checkRefusal(t, f, err, path, refused, nil)
	})
	t.Run("Create", func(t *testing.T) {
		dir := filepath.Join(base, "later")
		var f *KeyFile
		if child {
			f = openKeyless(t, filepath.Join(dir, "sealing.key"))
			awaitParent(t)
		} else {
			// No directory can turn another user's after an unprivileged
			// OpenKeyFile, so this is the keyless KeyFile it answers where
			// nothing is at the path.
			dir = closed
			f = &KeyFile{path: filepath.Join(dir, "sealing.key"), src: &keySource{}, closing: make(chan struct{})}
			t.Cleanup(func() { _ = f.Close() })
		}
		checkErr(t, f.Create(t.Context()), dir, refused, []error{ErrKeyExists})
		if f.src.lease != nil {
			t.Error("the KeyFile holds the lease")
		}
		checkNoKey(t, f.Sealer())
	})
	t.Run("own directory it cannot search", func(t *testing.T) {
		// Under /tmp: the child inherits root's TMPDIR, which may be closed
		// to it, and cannot write its root-owned working directory.
		dir, err := os.MkdirTemp("/tmp", "sealed-own-")
		if err != nil {
			t.Fatalf("MkdirTemp: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700); _ = os.RemoveAll(dir) })
		path := writeKeyFile(t, dir, "sealed-key/v1\n", 0o600)
		if err := os.Chmod(dir, 0o600); err != nil {
			t.Fatalf("chmod %s: %v", dir, err)
		}
		f, err := OpenKeyFile(t.Context(), path)
		checkRefusal(t, f, err, path, []error{fs.ErrPermission}, []error{ErrUnsafeKeyFile, filesafe.ErrNotOwned})
	})
}

// checkForeignClosed fails unless dir is a directory another uid owns
// that this uid cannot enter.
func checkForeignClosed(t *testing.T, dir string) {
	t.Helper()
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("Lstat(%s): %v", dir, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || st.Uid == uint32(os.Geteuid()) {
		t.Fatalf("%s is %v, want another uid's directory", dir, fi.Mode())
	}
	if _, err := os.Lstat(filepath.Join(dir, "sealing.key")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Lstat in %s = %v, want a permission error: a directory this uid cannot enter", dir, err)
	}
}

// runAsNobody builds the tree in a root-owned 0755 directory under /tmp,
// not the suite's TMPDIR, which may be closed to other users: closed, a
// root-owned 0700 directory holding a key file, and, once the child is
// ready, later, made the same way. It fails unless the child, run as nobody,
// reports the test passed, so a -test.run matching nothing cannot pass.
func runAsNobody(t *testing.T) {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "sealed-nobody-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", base, err)
	}
	if err := os.Mkdir(filepath.Join(base, "closed"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeKeyFile(t, filepath.Join(base, "closed"), "sealed-key/v1\n", 0o600)
	bin := filepath.Join(base, "sealed.test")
	copyTestBinary(t, bin)
	cmd := exec.CommandContext(t.Context(), bin, "-test.v", "-test.count=1", "-test.run=^TestForeignKeyDir$")
	cmd.Dir = base
	cmd.Env = append(os.Environ(), nobodyChild+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: nobodyUID, Gid: nobodyUID}}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the child as nobody: %v", err)
	}
	var out strings.Builder
	lines := bufio.NewScanner(stdout)
	for lines.Scan() {
		out.WriteString(lines.Text() + "\n")
		if lines.Text() == nobodyReady {
			if err := os.Mkdir(filepath.Join(base, "later"), 0o700); err != nil {
				t.Errorf("mkdir later: %v", err)
			}
			_, _ = io.WriteString(stdin, "\n")
		}
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil || !strings.Contains(out.String(), "--- PASS: TestForeignKeyDir ") {
		t.Errorf("the child as nobody = %v, output:\n%s%s", err, out.String(), stderr.String())
	}
}

// copyTestBinary copies the running test binary to bin at 0755. A copy
// written while another test forks can stay open in that child and fail
// the exec with ETXTBSY (https://go.dev/issue/22315).
func copyTestBinary(t *testing.T, bin string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read the test binary: %v", err)
	}
	syscall.ForkLock.RLock()
	err = os.WriteFile(bin, data, 0o755)
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("copy the test binary: %v", err)
	}
}

// awaitParent tells runAsNobody the child is ready and waits until it has
// made later.
func awaitParent(t *testing.T) {
	t.Helper()
	if _, err := os.Stdout.WriteString(nobodyReady + "\n"); err != nil {
		t.Fatalf("telling the parent: %v", err)
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatalf("waiting for the parent: %v", err)
	}
}
