package filesafe

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

// Root passes every permission check these tests depend on, so a root suite
// runs them again in a copy of the test binary as nobody, over a tree whose
// path it names in nobodyBase. Linux only: the unprivileged fixtures are
// /root and /, which other systems lay out differently.
const (
	nobodyUID   = 65534
	nobodyBase  = "FILESAFE_TEST_NOBODY_BASE"
	nobodyReady = "filesafe-test: waiting for the parent"
)

// asNobody runs the rest of the calling top-level test without privilege. It
// returns the fixture tree's base and true in the child, "" and true in a
// suite that already runs unprivileged, and false in a root suite once it has
// built the tree with setup and run the test as nobody, calling between when
// the child calls awaitParent.
func asNobody(t *testing.T, setup func(t *testing.T, base string), between func(base string)) (string, bool) {
	t.Helper()
	if base := os.Getenv(nobodyBase); base != "" {
		return base, true
	}
	if os.Geteuid() != 0 {
		return "", true
	}
	base, bin := nobodyTree(t)
	setup(t, base)
	runAsNobody(t, bin, base, between)
	return "", false
}

// nobodyTree makes a root-owned 0755 directory under /tmp, not the suite's
// TMPDIR, which may be closed to every other user, and copies the test binary
// into it.
func nobodyTree(t *testing.T) (base, bin string) {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "filesafe-nobody-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", base, err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}
	bin = filepath.Join(base, "filesafe.test")
	// A copy written while another test forks can stay open in that child and
	// fail this exec with ETXTBSY (https://go.dev/issue/22315).
	syscall.ForkLock.RLock()
	err = copyExecutable(exe, bin)
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("copying the test binary: %v", err)
	}
	return base, bin
}

func copyExecutable(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

// runAsNobody runs the calling test in bin as nobody and fails unless the
// child reports it passed, so a -test.run that matches nothing cannot pass.
func runAsNobody(t *testing.T, bin, base string, between func(base string)) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), bin, "-test.v", "-test.count=1",
		"-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
	cmd.Dir = base
	cmd.Env = append(os.Environ(), nobodyBase+"="+base)
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
			if between != nil {
				between(base)
			}
			_, _ = io.WriteString(stdin, "\n")
		}
	}
	_ = stdin.Close()
	err = cmd.Wait()
	if err != nil || !strings.Contains(out.String(), "--- PASS: "+t.Name()+" ") {
		t.Errorf("the child as nobody = %v, output:\n%s%s", err, out.String(), stderr.String())
	}
}

// awaitParent tells runAsNobody the child is ready for between and waits for
// it to have run.
func awaitParent(t *testing.T) {
	t.Helper()
	if _, err := os.Stdout.WriteString(nobodyReady + "\n"); err != nil {
		t.Fatalf("telling the parent: %v", err)
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatalf("waiting for the parent: %v", err)
	}
}

// closedForeignDirs builds, for a root suite, two root-owned directories a
// nobody child can neither list nor, at 0700, enter.
func closedForeignDirs(t *testing.T, base string) {
	mkdirExact(t, filepath.Join(base, "foreign"), 0o700)
	mkdirExact(t, filepath.Join(base, "foreign-711"), 0o711)
}

// closedForeign answers the directory holding the closed foreign directories
// and their names: closedForeignDirs' in the child, and /root, which every
// Linux keeps at 0700 for root, in an unprivileged suite.
func closedForeign(t *testing.T, base string) (string, []string) {
	t.Helper()
	if base != "" {
		return base, []string{"foreign", "foreign-711"}
	}
	const dir = "/root"
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 || ownerErr(fi, dir) == nil {
		t.Fatalf("Lstat(%s) = (%v, %v), want another uid's directory closed to group and other", dir, fi, err)
	}
	return "/", []string{"root"}
}

func assertNotOwnedAtOpen(t *testing.T, call string, err error) {
	t.Helper()
	if !errors.Is(err, ErrNotOwned) {
		t.Errorf("%s = %v, want ErrNotOwned", call, err)
	}
	if !errors.Is(err, syscall.EACCES) || !errors.Is(err, fs.ErrPermission) {
		t.Errorf("%s = %v, want it to wrap the open's EACCES", call, err)
	}
}

func openRootAt(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestEnsurePrivateDir_RefusesAClosedForeignDirAsNotOwned(t *testing.T) {
	base, ok := asNobody(t, closedForeignDirs, nil)
	if !ok {
		return
	}
	parent, names := closedForeign(t, base)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(parent, name)
			_, err := EnsurePrivateDir(dir)
			assertNotOwnedAtOpen(t, "EnsurePrivateDir("+dir+")", err)
		})
	}
}

func TestCheckPrivateDir_RefusesAClosedForeignDirAsNotOwned(t *testing.T) {
	base, ok := asNobody(t, closedForeignDirs, nil)
	if !ok {
		return
	}
	parent, names := closedForeign(t, base)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(parent, name)
			assertNotOwnedAtOpen(t, "CheckPrivateDir("+dir+")", CheckPrivateDir(dir))
		})
	}
}

func TestCheckPrivateDirInRoot_RefusesAClosedForeignDirAsNotOwned(t *testing.T) {
	base, ok := asNobody(t, closedForeignDirs, nil)
	if !ok {
		return
	}
	parent, names := closedForeign(t, base)
	root := openRootAt(t, parent)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			assertNotOwnedAtOpen(t, "CheckPrivateDirInRoot("+parent+", "+name+")", CheckPrivateDirInRoot(root, name))
		})
	}
}

// The child opens a root over each directory while nobody owns it, and the
// parent then hands both to root: at 0700 nothing inside can be looked up,
// and at 0711 nothing can be listed.
func TestCheckPrivateDirInRoot_RefusesItsOwnDirectoryOnceAnotherUidOwnsIt(t *testing.T) {
	setup := func(t *testing.T, base string) {
		for _, name := range []string{"mine", "mine-711"} {
			dir := filepath.Join(base, name)
			mkdirExact(t, dir, 0o700)
			if err := os.Chown(dir, nobodyUID, nobodyUID); err != nil {
				t.Fatalf("chown %s: %v", dir, err)
			}
		}
	}
	between := func(base string) {
		for _, name := range []string{"mine", "mine-711"} {
			if err := os.Chown(filepath.Join(base, name), 0, 0); err != nil {
				t.Errorf("chown %s back to root: %v", name, err)
			}
		}
		if err := os.Chmod(filepath.Join(base, "mine-711"), 0o711); err != nil {
			t.Errorf("chmod mine-711: %v", err)
		}
	}
	base, ok := asNobody(t, setup, between)
	if !ok {
		return
	}
	if base == "" {
		// Unprivileged, no root this process can open can be taken from it,
		// so this pins only the verdict on an open root another uid owns.
		if err := CheckPrivateDirInRoot(openRootAt(t, "/"), "."); !errors.Is(err, ErrNotOwned) {
			t.Errorf("CheckPrivateDirInRoot(/, .) = %v, want ErrNotOwned", err)
		}
		return
	}
	roots := map[string]*os.Root{}
	for _, name := range []string{"mine", "mine-711"} {
		roots[name] = openRootAt(t, filepath.Join(base, name))
		if err := CheckPrivateDirInRoot(roots[name], "."); err != nil {
			t.Fatalf("CheckPrivateDirInRoot(%s, .) while nobody owns it = %v, want nil", name, err)
		}
	}
	awaitParent(t)
	for name, root := range roots {
		t.Run(name, func(t *testing.T) {
			assertNotOwnedAtOpen(t, "CheckPrivateDirInRoot("+name+", .)", CheckPrivateDirInRoot(root, "."))
		})
	}
}

// A parent this process cannot read or enter is not the directory asked
// about, so it stays a permission error and never becomes ErrNotOwned, even
// when another uid owns it.
func TestPrivateDir_AnswersAPermissionErrorForAParentItCannotRead(t *testing.T) {
	setup := func(t *testing.T, base string) {
		closedForeignDirs(t, base)
		parent := filepath.Join(base, "traverse-only")
		mkdirExact(t, parent, 0o700)
		if err := os.Chown(parent, nobodyUID, nobodyUID); err != nil {
			t.Fatalf("chown %s: %v", parent, err)
		}
		if err := os.Chmod(parent, 0o300); err != nil {
			t.Fatalf("chmod %s: %v", parent, err)
		}
	}
	base, ok := asNobody(t, setup, nil)
	if !ok {
		return
	}
	traverseOnly := filepath.Join(base, "traverse-only")
	if base == "" {
		traverseOnly = filepath.Join(t.TempDir(), "traverse-only")
		mkdirExact(t, traverseOnly, 0o300)
		t.Cleanup(func() { _ = os.Chmod(traverseOnly, 0o700) })
	}
	rootDir, names := closedForeign(t, base)
	foreign := filepath.Join(rootDir, names[0])

	assertPermission := func(t *testing.T, call string, err error) {
		t.Helper()
		if !errors.Is(err, fs.ErrPermission) {
			t.Errorf("%s = %v, want fs.ErrPermission", call, err)
		}
		if errors.Is(err, ErrNotOwned) || errors.Is(err, ErrParentNotSynced) {
			t.Errorf("%s = %v, want neither ErrNotOwned nor ErrParentNotSynced", call, err)
		}
	}
	t.Run("EnsurePrivateDir under a traverse-only parent", func(t *testing.T) {
		dir := filepath.Join(traverseOnly, "credentials")
		_, err := EnsurePrivateDir(dir)
		assertPermission(t, "EnsurePrivateDir("+dir+")", err)
	})
	t.Run("EnsurePrivateDir under a closed foreign parent", func(t *testing.T) {
		dir := filepath.Join(foreign, "credentials")
		_, err := EnsurePrivateDir(dir)
		assertPermission(t, "EnsurePrivateDir("+dir+")", err)
	})
	t.Run("CheckPrivateDir under a closed foreign parent", func(t *testing.T) {
		dir := filepath.Join(foreign, "credentials")
		assertPermission(t, "CheckPrivateDir("+dir+")", CheckPrivateDir(dir))
	})
	t.Run("CheckPrivateDirInRoot under a closed foreign parent", func(t *testing.T) {
		name := filepath.Join(names[0], "credentials")
		assertPermission(t, "CheckPrivateDirInRoot("+name+")", CheckPrivateDirInRoot(openRootAt(t, rootDir), name))
	})
}
