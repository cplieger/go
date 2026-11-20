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

func assertNotOwnedAtOpen(t *testing.T, call string, err error, name string) {
	t.Helper()
	assertNotOwned(t, call, err, name)
	if !errors.Is(err, syscall.EACCES) || !errors.Is(err, fs.ErrPermission) {
		t.Errorf("%s = %v, want it to wrap the open's EACCES", call, err)
	}
}

func assertPlainPermission(t *testing.T, call string, err error) {
	t.Helper()
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("%s = %v, want fs.ErrPermission", call, err)
	}
	if errors.Is(err, ErrNotOwned) || errors.Is(err, ErrParentNotSynced) || errors.Is(err, ErrRaced) {
		t.Errorf("%s = %v, want none of ErrNotOwned, ErrParentNotSynced and ErrRaced", call, err)
	}
}

// ownWorkspace builds, for a root suite, the directory nobody owns where the
// child makes the fixtures it must own.
func ownWorkspace(t *testing.T, base string) {
	dir := filepath.Join(base, "own")
	mkdirExact(t, dir, 0o700)
	if err := os.Chown(dir, nobodyUID, nobodyUID); err != nil {
		t.Fatalf("chown %s: %v", dir, err)
	}
}

// ownDir answers a directory the running uid owns: ownWorkspace's in the
// child, a fresh temporary directory in an unprivileged suite.
func ownDir(t *testing.T, base string) string {
	t.Helper()
	if base == "" {
		return t.TempDir()
	}
	return filepath.Join(base, "own")
}

// closeDir sets dir to mode 0 and gives it back its mode at cleanup, so the
// suite can remove what it holds.
func closeDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatalf("chmod %s to 0: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// rootOwnedDir answers a directory root owns and the running uid can search:
// the tree's base in the child, / in an unprivileged suite.
func rootOwnedDir(base string) string {
	if base == "" {
		return "/"
	}
	return base
}

// linkedRoot makes dir at 0700 and opens a root on it through a symlink, so
// the test can repoint the root's path while the root keeps dir.
func linkedRoot(t *testing.T, dir string) (root *os.Root, link string) {
	t.Helper()
	mkdirExact(t, dir, 0o700)
	link = dir + ".link"
	if err := os.Symlink(dir, link); err != nil {
		t.Fatalf("symlink %s: %v", link, err)
	}
	return openRootAt(t, link), link
}

func repoint(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove %s: %v", link, err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s to %s: %v", link, target, err)
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
			assertNotOwnedAtOpen(t, "EnsurePrivateDir("+dir+")", err, dir)
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
			assertNotOwnedAtOpen(t, "CheckPrivateDir("+dir+")", CheckPrivateDir(dir), dir)
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
			assertNotOwnedAtOpen(t, "CheckPrivateDirInRoot("+parent+", "+name+")", CheckPrivateDirInRoot(root, name), name)
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
	// A refused lookup is named by the root's path; at 0711 the lookup
	// passes and the open of "." is what is refused.
	wantName := map[string]string{"mine": filepath.Join(base, "mine"), "mine-711": "."}
	awaitParent(t)
	for name, root := range roots {
		t.Run(name, func(t *testing.T) {
			assertNotOwnedAtOpen(t, "CheckPrivateDirInRoot("+name+", .)", CheckPrivateDirInRoot(root, "."), wantName[name])
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

	t.Run("EnsurePrivateDir under a traverse-only parent", func(t *testing.T) {
		dir := filepath.Join(traverseOnly, "credentials")
		_, err := EnsurePrivateDir(dir)
		assertPlainPermission(t, "EnsurePrivateDir("+dir+")", err)
	})
	t.Run("EnsurePrivateDir under a closed foreign parent", func(t *testing.T) {
		dir := filepath.Join(foreign, "credentials")
		_, err := EnsurePrivateDir(dir)
		assertPlainPermission(t, "EnsurePrivateDir("+dir+")", err)
	})
	t.Run("CheckPrivateDir under a closed foreign parent", func(t *testing.T) {
		dir := filepath.Join(foreign, "credentials")
		assertPlainPermission(t, "CheckPrivateDir("+dir+")", CheckPrivateDir(dir))
	})
	t.Run("CheckPrivateDirInRoot under a closed foreign parent", func(t *testing.T) {
		name := filepath.Join(names[0], "credentials")
		assertPlainPermission(t, "CheckPrivateDirInRoot("+name+")", CheckPrivateDirInRoot(openRootAt(t, rootDir), name))
	})
}

// A directory the caller owns whose own mode shuts the caller out stays a
// permission error: ErrNotOwned names only a refusal by another uid's mode.
func TestPrivateDir_AnswersAPermissionErrorForAClosedDirItOwns(t *testing.T) {
	base, ok := asNobody(t, ownWorkspace, nil)
	if !ok {
		return
	}
	own := ownDir(t, base)
	closed := filepath.Join(own, "closed")
	mkdirExact(t, closed, 0o700)
	rootDir := filepath.Join(own, "root")
	mkdirExact(t, rootDir, 0o700)
	ownRoot := openRootAt(t, own)
	closedRoot := openRootAt(t, rootDir)
	closeDir(t, closed)
	closeDir(t, rootDir)

	t.Run("EnsurePrivateDir", func(t *testing.T) {
		_, err := EnsurePrivateDir(closed)
		assertPlainPermission(t, "EnsurePrivateDir("+closed+")", err)
	})
	t.Run("CheckPrivateDir", func(t *testing.T) {
		assertPlainPermission(t, "CheckPrivateDir("+closed+")", CheckPrivateDir(closed))
	})
	t.Run("CheckPrivateDirInRoot on a child", func(t *testing.T) {
		assertPlainPermission(t, "CheckPrivateDirInRoot("+own+", closed)", CheckPrivateDirInRoot(ownRoot, "closed"))
	})
	t.Run("CheckPrivateDirInRoot on its own directory", func(t *testing.T) {
		assertPlainPermission(t, "CheckPrivateDirInRoot("+rootDir+", .)", CheckPrivateDirInRoot(closedRoot, "."))
	})
}

// For "." on a root closed to the caller, the owner is that of the directory
// the root's path names now, so a path repointed at another uid's directory
// is labelled from that directory.
func TestCheckPrivateDirInRoot_ReadsAClosedRootsOwnerFromItsPath(t *testing.T) {
	base, ok := asNobody(t, ownWorkspace, nil)
	if !ok {
		return
	}
	dir := filepath.Join(ownDir(t, base), "mine")
	root, link := linkedRoot(t, dir)
	closeDir(t, dir)
	repoint(t, link, rootOwnedDir(base))
	assertNotOwnedAtOpen(t, "CheckPrivateDirInRoot("+link+", .)", CheckPrivateDirInRoot(root, "."), link)
}

// A refused open is classified from a lookup taken after it, not from the
// Lstat before it, so what changed in between names the refusal.
func TestCheckPrivateDirInRoot_ClassifiesARefusedOpenByALookupAfterIt(t *testing.T) {
	setup := func(t *testing.T, base string) {
		ownWorkspace(t, base)
		dir := filepath.Join(base, "own", "turned")
		mkdirExact(t, dir, 0o700)
		if err := os.Chown(dir, nobodyUID, nobodyUID); err != nil {
			t.Fatalf("chown %s: %v", dir, err)
		}
	}
	between := func(base string) {
		if err := os.Chown(filepath.Join(base, "own", "turned"), 0, 0); err != nil {
			t.Errorf("chown turned to root: %v", err)
		}
	}
	base, ok := asNobody(t, setup, between)
	if !ok {
		return
	}
	own := ownDir(t, base)

	t.Run("its own directory closed and its path repointed", func(t *testing.T) {
		dir := filepath.Join(own, "mine")
		root, link := linkedRoot(t, dir)
		withPinOpenHook(t, func(string) {
			closeDir(t, dir)
			repoint(t, link, rootOwnedDir(base))
		})
		assertNotOwnedAtOpen(t, "CheckPrivateDirInRoot("+link+", .)", CheckPrivateDirInRoot(root, "."), link)
	})
	if base == "" {
		// Unprivileged, no directory can be handed to another uid.
		return
	}
	t.Run("a child another uid took over", func(t *testing.T) {
		root := openRootAt(t, own)
		withPinOpenHook(t, func(string) { awaitParent(t) })
		assertNotOwnedAtOpen(t, "CheckPrivateDirInRoot("+own+", turned)", CheckPrivateDirInRoot(root, "turned"), "turned")
	})
}

// A refused open of a name swapped since its Lstat is ErrRaced, the refusal a
// retry can cure, even when the open's errno is a permission error.
func TestCheckPrivateDirInRoot_AnswersErrRacedForANameSwappedBeforeARefusedOpen(t *testing.T) {
	base, ok := asNobody(t, ownWorkspace, nil)
	if !ok {
		return
	}
	own := ownDir(t, base)
	dir := filepath.Join(own, "swapped")
	moved := filepath.Join(own, "swapped-away")
	for _, d := range []string{dir, moved} {
		t.Cleanup(func() { _ = os.RemoveAll(d) })
		t.Cleanup(func() { _ = os.Chmod(d, 0o700) })
	}
	mkdirExact(t, dir, 0)
	root := openRootAt(t, own)
	withPinOpenHook(t, func(string) {
		mustRename(t, dir, moved)
		mkdirExact(t, dir, 0)
	})
	err := CheckPrivateDirInRoot(root, "swapped")
	if !errors.Is(err, ErrRaced) || !errors.Is(err, fs.ErrPermission) {
		t.Errorf("CheckPrivateDirInRoot(%s, swapped) = %v, want ErrRaced wrapping fs.ErrPermission", own, err)
	}
}

// A closed root whose directory moved off its path can only be refused: the
// path then names nothing, or a directory the root does not hold, and neither
// can turn the refusal into a pass.
func TestCheckPrivateDirInRoot_RefusesAClosedRootWhoseDirectoryMoved(t *testing.T) {
	base, ok := asNobody(t, ownWorkspace, nil)
	if !ok {
		return
	}
	own := ownDir(t, base)
	for name, refill := range map[string]func(t *testing.T, path string){
		"nothing at its path": func(*testing.T, string) {},
		"a private directory it owns at its path": func(t *testing.T, path string) {
			mkdirExact(t, path, 0o700)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(own, "moving")
			mkdirExact(t, dir, 0o700)
			root := openRootAt(t, dir)
			away, err := os.MkdirTemp(own, "away-")
			if err != nil {
				t.Fatalf("MkdirTemp: %v", err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(away) })
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			moved := filepath.Join(away, "moving")
			mustRename(t, dir, moved)
			closeDir(t, moved)
			refill(t, dir)
			assertPlainPermission(t, "CheckPrivateDirInRoot(moved root, .)", CheckPrivateDirInRoot(root, "."))
		})
	}
}

// assertNotOwned requires err to be a *NotOwnedError naming name, owned by
// root and compared with the effective uid.
func assertNotOwned(t *testing.T, call string, err error, name string) {
	t.Helper()
	if !errors.Is(err, ErrNotOwned) {
		t.Errorf("%s = %v, want it to match ErrNotOwned", call, err)
	}
	no, ok := errors.AsType[*NotOwnedError](err)
	if !ok {
		t.Fatalf("%s = %v, want a *NotOwnedError", call, err)
	}
	if no.Name != name || no.Owner != 0 || no.EUID != uidOf(os.Geteuid()) {
		t.Errorf("%s: NotOwnedError = %+v, want {Name: %s, Owner: 0, EUID: %d}", call, *no, name, uidOf(os.Geteuid()))
	}
}

// A root-owned 0755 directory opens for any uid, so each check reaches the
// owner comparison on its open handle; the parent then hands the directory
// to nobody, which must not change a refusal already returned.
func TestNotOwnedError_CarriesBothUidsEachPrivateDirCheckCompared(t *testing.T) {
	setup := func(t *testing.T, base string) { mkdirExact(t, filepath.Join(base, "foreign"), 0o755) }
	between := func(base string) {
		if err := os.Chown(filepath.Join(base, "foreign"), nobodyUID, nobodyUID); err != nil {
			t.Errorf("chown foreign to nobody: %v", err)
		}
	}
	base, ok := asNobody(t, setup, between)
	if !ok {
		return
	}
	dir, rootDir, name := "/", "/", "."
	if base != "" {
		dir, rootDir, name = filepath.Join(base, "foreign"), base, "foreign"
	}
	_, ensureErr := EnsurePrivateDir(dir)
	got := []struct {
		err        error
		call, name string
	}{
		{ensureErr, "EnsurePrivateDir", dir},
		{CheckPrivateDir(dir), "CheckPrivateDir", dir},
		{CheckPrivateDirInRoot(openRootAt(t, rootDir), name), "CheckPrivateDirInRoot", name},
	}
	msgs := make([]string, len(got))
	for i, g := range got {
		assertNotOwned(t, g.call, g.err, g.name)
		msgs[i] = g.err.Error()
	}
	if base == "" {
		return
	}
	awaitParent(t)
	for i, g := range got {
		assertNotOwned(t, g.call+" after the chown", g.err, g.name)
		if g.err.Error() != msgs[i] {
			t.Errorf("%s message after the chown = %q, want %q unchanged", g.call, g.err.Error(), msgs[i])
		}
	}
}

// Without CAP_FSETID, fchmod drops setgid from a file whose group the caller
// is not in (chmod(2)), so EnforceMode reads back less than it asked for.
// Only root can make such a file for an unprivileged child.
func TestModeNotStoredError_CarriesBothModesEnforceModeCompared(t *testing.T) {
	setup := func(t *testing.T, base string) {
		ownWorkspace(t, base)
		path := filepath.Join(base, "own", "f")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, nobodyUID, 0); err != nil {
			t.Fatal(err)
		}
	}
	base, ok := asNobody(t, setup, nil)
	if !ok {
		return
	}
	if base == "" {
		t.Skip("needs a file whose group the process is not in, which only a root suite can make")
	}
	path := filepath.Join(base, "own", "f")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	stored, err := EnforceMode(f, os.ModeSetgid|0o600)
	if !errors.Is(err, ErrModeNotStored) {
		t.Errorf("EnforceMode = %v, want it to match ErrModeNotStored", err)
	}
	mn, ok := errors.AsType[*ModeNotStoredError](err)
	if !ok {
		t.Fatalf("EnforceMode = %v, want a *ModeNotStoredError", err)
	}
	if mn.Name != path || mn.Requested != os.ModeSetgid|0o600 || mn.Stored != 0o600 || stored != 0o600 {
		t.Errorf("ModeNotStoredError = %+v, stored %v; want {Name: %s, Requested: setgid 0600, Stored: 0600}", *mn, stored, path)
	}
	want := "filesafe: filesystem did not store the requested mode: " + path + ": asked for 02600, filesystem stored 0600"
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
	if err := f.Chmod(0o644); err != nil {
		t.Fatal(err)
	}
	if mn.Stored != 0o600 || mn.Requested != os.ModeSetgid|0o600 {
		t.Errorf("after chmod 0644: ModeNotStoredError = %+v, want the modes EnforceMode compared", *mn)
	}
}
