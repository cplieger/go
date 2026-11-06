//go:build !windows

package filesafe_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/go/filesafe"
)

func ExampleWriteFile_withMode() {
	dir, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "secret.txt")
	_, _ = filesafe.WriteFile(context.Background(), path, []byte("s3cr3t"),
		filesafe.WithMode(0o600))
	fi, _ := os.Stat(path)
	fmt.Println(fi.Mode().Perm())
	// Output: -rw-------
}

func ExampleWriteFile_withMkdirMode() {
	dir, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "nested", "dir", "file.txt")
	_, _ = filesafe.WriteFile(context.Background(), path, []byte("deep"),
		filesafe.WithMkdirMode(0o755))
	data, _ := os.ReadFile(path)
	fmt.Println(string(data))
	// Output: deep
}

func ExampleWriteReader_withMode() {
	dir, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "stream.txt")
	_, _ = filesafe.WriteReader(context.Background(), path,
		strings.NewReader("streamed"), filesafe.WithMode(0o644))
	data, _ := os.ReadFile(path)
	fmt.Println(string(data))
	// Output: streamed
}

func ExampleEnforceMode() {
	dir, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "secret.json")
	_ = os.WriteFile(path, []byte(`{}`), 0o644)

	f, _ := os.Open(path)
	defer f.Close()

	// The mode a create or a chmod takes is a REQUEST; the returned mode is what
	// the filesystem stored, read back from the same descriptor that was chmod'ed.
	stored, err := filesafe.EnforceMode(f, 0o600)
	fmt.Printf("stored %#o, err %v\n", stored, err)
	// Output: stored 0600, err <nil>
}

func ExampleEnsurePrivateDir() {
	base, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(base)

	// One call establishes ONE level, so a multi-level path is the caller's loop,
	// outermost first: each level gets its own verdict.
	root := filepath.Join(base, "app")
	state := filepath.Join(root, "state")
	for _, dir := range []string{root, state} {
		pd, err := filesafe.EnsurePrivateDir(dir)
		if err != nil {
			fmt.Println("refused:", err)
			return
		}
		fmt.Printf("%s created=%t mode=%#o\n", filepath.Base(dir), pd.Created, pd.Mode)
	}

	// A second run adopts what the first one left, and never chmods it.
	pd, _ := filesafe.EnsurePrivateDir(state)
	fmt.Printf("second run created=%t mode=%#o\n", pd.Created, pd.Mode)
	// Output:
	// app created=true mode=0700
	// state created=true mode=0700
	// second run created=false mode=0700
}

func ExampleEnsurePrivateDir_refusals() {
	base, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(base)

	// A symlink planted at the name is refused by the kernel, not followed.
	link := filepath.Join(base, "link")
	_ = os.Symlink(base, link)
	_, err := filesafe.EnsurePrivateDir(link)
	fmt.Println("symlink:", errors.Is(err, filesafe.ErrSymlinkTarget))

	// A pre-existing directory somebody else could enter is refused, never
	// repaired: chmod'ing another principal's directory would take over its name.
	shared := filepath.Join(base, "shared")
	_ = os.Mkdir(shared, 0o755)
	_ = os.Chmod(shared, 0o755)
	_, err = filesafe.EnsurePrivateDir(shared)
	fmt.Println("group/other access:", errors.Is(err, filesafe.ErrModeTooOpen))
	// Output:
	// symlink: true
	// group/other access: true
}
