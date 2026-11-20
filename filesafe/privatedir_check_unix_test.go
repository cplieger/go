//go:build !windows

package filesafe

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openPrivateTestRoot makes a 0700 directory and opens it as a root.
func openPrivateTestRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "root")
	mkdirExact(t, dir, 0o700)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, dir
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat(%s) = %v, want fs.ErrNotExist: nothing may be created", path, err)
	}
}

func TestCheckPrivateDir_AcceptsAPrivateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "priv")
	mkdirExact(t, dir, 0o700)
	synced := recordFsyncDir(t)

	if err := CheckPrivateDir(dir); err != nil {
		t.Errorf("CheckPrivateDir = %v, want nil", err)
	}
	if got := synced(); len(got) != 0 {
		t.Errorf("synced dirs = %q, want none", got)
	}
}

func TestCheckPrivateDir_MissingIsNotExistAndCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")

	if err := CheckPrivateDir(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("CheckPrivateDir = %v, want fs.ErrNotExist", err)
	}
	assertNotExist(t, dir)
}

func TestCheckPrivateDir_RefusesWhatIsNotAPrivateDirectory(t *testing.T) {
	t.Run("symlink to a private directory", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "real")
		mkdirExact(t, target, 0o700)
		link := filepath.Join(parent, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := CheckPrivateDir(link); !errors.Is(err, ErrSymlinkTarget) {
			t.Errorf("CheckPrivateDir(symlink) = %v, want ErrSymlinkTarget", err)
		}
	})
	t.Run("regular file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := CheckPrivateDir(file); !errors.Is(err, ErrNotDirectory) {
			t.Errorf("CheckPrivateDir(file) = %v, want ErrNotDirectory", err)
		}
	})
	t.Run("group-readable directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "wide")
		mkdirExact(t, dir, 0o750)
		synced := recordFsyncDir(t)
		if err := CheckPrivateDir(dir); !errors.Is(err, ErrModeTooOpen) {
			t.Errorf("CheckPrivateDir(0750) = %v, want ErrModeTooOpen", err)
		}
		if got := permOf(t, dir); got != 0o750 {
			t.Errorf("mode after check = %#o, want 0750 unchanged", got)
		}
		if got := synced(); len(got) != 0 {
			t.Errorf("synced dirs = %q, want none", got)
		}
	})
	t.Run("foreign owner", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("needs privilege to create a directory owned by another uid")
		}
		dir := filepath.Join(t.TempDir(), "foreign")
		mkdirExact(t, dir, 0o700)
		if err := os.Chown(dir, 65534, 65534); err != nil {
			t.Fatal(err)
		}
		if err := CheckPrivateDir(dir); !errors.Is(err, ErrNotOwned) {
			t.Errorf("CheckPrivateDir(foreign-owned) = %v, want ErrNotOwned", err)
		}
	})
}

func TestCheckPrivateDir_RejectsBadPaths(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if err := CheckPrivateDir(""); !errors.Is(err, ErrEmptyPath) {
			t.Errorf("CheckPrivateDir(\"\") = %v, want ErrEmptyPath", err)
		}
	})
	t.Run("relative", func(t *testing.T) {
		if err := CheckPrivateDir("rel/dir"); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("CheckPrivateDir(relative) = %v, want ErrUnsafePath", err)
		}
	})
}

func TestCheckPrivateDirInRoot_AcceptsAPrivateDirectory(t *testing.T) {
	t.Run("the root itself", func(t *testing.T) {
		root, _ := openPrivateTestRoot(t)
		synced := recordFsyncDir(t)
		if err := CheckPrivateDirInRoot(root, "."); err != nil {
			t.Errorf("CheckPrivateDirInRoot(.) = %v, want nil", err)
		}
		if got := synced(); len(got) != 0 {
			t.Errorf("synced dirs = %q, want none", got)
		}
	})
	t.Run("a named subdirectory", func(t *testing.T) {
		root, dir := openPrivateTestRoot(t)
		mkdirExact(t, filepath.Join(dir, "sub"), 0o700)
		if err := CheckPrivateDirInRoot(root, "sub"); err != nil {
			t.Errorf("CheckPrivateDirInRoot(sub) = %v, want nil", err)
		}
	})
}

func TestCheckPrivateDirInRoot_ChecksTheRootAsItIsNow(t *testing.T) {
	root, dir := openPrivateTestRoot(t)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := CheckPrivateDirInRoot(root, "."); !errors.Is(err, ErrModeTooOpen) {
		t.Errorf("CheckPrivateDirInRoot(.) after chmod 0755 = %v, want ErrModeTooOpen", err)
	}
	if got := permOf(t, dir); got != 0o755 {
		t.Errorf("mode after check = %#o, want 0755 unchanged", got)
	}
}

func TestCheckPrivateDirInRoot_MissingIsNotExistAndCreatesNothing(t *testing.T) {
	root, dir := openPrivateTestRoot(t)

	if err := CheckPrivateDirInRoot(root, "absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("CheckPrivateDirInRoot = %v, want fs.ErrNotExist", err)
	}
	assertNotExist(t, filepath.Join(dir, "absent"))
}

func TestCheckPrivateDirInRoot_RefusesWhatIsNotAPrivateDirectory(t *testing.T) {
	t.Run("symlink to a private directory", func(t *testing.T) {
		root, dir := openPrivateTestRoot(t)
		mkdirExact(t, filepath.Join(dir, "real"), 0o700)
		if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
			t.Fatal(err)
		}
		if err := CheckPrivateDirInRoot(root, "link"); !errors.Is(err, ErrSymlinkTarget) {
			t.Errorf("CheckPrivateDirInRoot(symlink) = %v, want ErrSymlinkTarget", err)
		}
	})
	t.Run("regular file", func(t *testing.T) {
		root, dir := openPrivateTestRoot(t)
		if err := os.WriteFile(filepath.Join(dir, "f"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := CheckPrivateDirInRoot(root, "f"); !errors.Is(err, ErrNotDirectory) {
			t.Errorf("CheckPrivateDirInRoot(file) = %v, want ErrNotDirectory", err)
		}
	})
	t.Run("group-readable subdirectory", func(t *testing.T) {
		root, dir := openPrivateTestRoot(t)
		sub := filepath.Join(dir, "wide")
		mkdirExact(t, sub, 0o750)
		if err := CheckPrivateDirInRoot(root, "wide"); !errors.Is(err, ErrModeTooOpen) {
			t.Errorf("CheckPrivateDirInRoot(0750) = %v, want ErrModeTooOpen", err)
		}
		if got := permOf(t, sub); got != 0o750 {
			t.Errorf("mode after check = %#o, want 0750 unchanged", got)
		}
	})
	t.Run("foreign owner", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("needs privilege to create a directory owned by another uid")
		}
		root, dir := openPrivateTestRoot(t)
		sub := filepath.Join(dir, "foreign")
		mkdirExact(t, sub, 0o700)
		if err := os.Chown(sub, 65534, 65534); err != nil {
			t.Fatal(err)
		}
		if err := CheckPrivateDirInRoot(root, "foreign"); !errors.Is(err, ErrNotOwned) {
			t.Errorf("CheckPrivateDirInRoot(foreign-owned) = %v, want ErrNotOwned", err)
		}
	})
}

func TestCheckPrivateDirInRoot_RejectsBadArguments(t *testing.T) {
	t.Run("nil root", func(t *testing.T) {
		if err := CheckPrivateDirInRoot(nil, "."); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("CheckPrivateDirInRoot(nil) = %v, want ErrUnsafePath", err)
		}
	})
	t.Run("empty name", func(t *testing.T) {
		root, _ := openPrivateTestRoot(t)
		if err := CheckPrivateDirInRoot(root, ""); !errors.Is(err, ErrEmptyPath) {
			t.Errorf("CheckPrivateDirInRoot(\"\") = %v, want ErrEmptyPath", err)
		}
	})
	t.Run("absolute name", func(t *testing.T) {
		root, dir := openPrivateTestRoot(t)
		if err := CheckPrivateDirInRoot(root, dir); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("CheckPrivateDirInRoot(absolute) = %v, want ErrUnsafePath", err)
		}
	})
}

// TestCheckPrivateDirInRoot_SwappedNameIsErrRaced swaps the checked name for
// another private directory, or a symlink to one, between its Lstat and its
// open: either passes every check on its own, so only the identity check
// catches it. Serial: the hook is package state.
func TestCheckPrivateDirInRoot_SwappedNameIsErrRaced(t *testing.T) {
	for name, swap := range map[string]func(t *testing.T, dir string){
		"another private directory": func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "sub"), filepath.Join(dir, "sub.old"))
			mkdirExact(t, filepath.Join(dir, "sub"), 0o700)
		},
		"a symlink to a private sibling": func(t *testing.T, dir string) {
			mkdirExact(t, filepath.Join(dir, "sib"), 0o700)
			mustRename(t, filepath.Join(dir, "sub"), filepath.Join(dir, "sub.old"))
			if err := os.Symlink("sib", filepath.Join(dir, "sub")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, dir := openPrivateTestRoot(t)
			mkdirExact(t, filepath.Join(dir, "sub"), 0o700)
			withPinOpenHook(t, func(string) { swap(t, dir) })

			if err := CheckPrivateDirInRoot(root, "sub"); !errors.Is(err, ErrRaced) {
				t.Errorf("CheckPrivateDirInRoot(swapped) = %v, want ErrRaced", err)
			}
		})
	}
}

// The mode each check refuses is the one it read, sticky bit included, and
// stays that after the directory is widened further. The message renders it
// in chmod(1) octal.
func TestModeTooOpenError_CarriesTheModeEachPrivateDirCheckRead(t *testing.T) {
	for _, tc := range []struct {
		octal string
		mode  os.FileMode
	}{
		{"0750", 0o750},
		{"0705", 0o705},
		{"01770", os.ModeSticky | 0o770},
	} {
		t.Run(tc.octal, func(t *testing.T) {
			check := func(t *testing.T, err error, path, name string) {
				t.Helper()
				if want := "(mode " + tc.octal + ")"; err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want a message containing %q", err, want)
				}
				assertModeTooOpen(t, err, path, name, tc.mode)
			}
			t.Run("EnsurePrivateDir", func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "wide")
				mkdirExact(t, dir, tc.mode)
				_, err := EnsurePrivateDir(dir)
				check(t, err, dir, dir)
			})
			t.Run("CheckPrivateDir", func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "wide")
				mkdirExact(t, dir, tc.mode)
				check(t, CheckPrivateDir(dir+"/"), dir, dir)
			})
			t.Run("CheckPrivateDirInRoot", func(t *testing.T) {
				root, rootDir := openPrivateTestRoot(t)
				dir := filepath.Join(rootDir, "wide")
				mkdirExact(t, dir, tc.mode)
				check(t, CheckPrivateDirInRoot(root, "./wide"), dir, "wide")
			})
			t.Run("CheckPrivateDirInRoot of the root itself", func(t *testing.T) {
				root, rootDir := openPrivateTestRoot(t)
				if err := os.Chmod(rootDir, tc.mode); err != nil {
					t.Fatal(err)
				}
				check(t, CheckPrivateDirInRoot(root, "."), rootDir, ".")
			})
		})
	}
}
