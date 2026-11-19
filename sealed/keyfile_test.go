package sealed

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/cplieger/go/filesafe"
)

// TestZeroKeyFile checks the zero KeyFile against its godoc ("The zero
// KeyFile has no path: its Sealer holds no key, its writes fail with
// filesafe.ErrEmptyPath, and its Close answers nil") and Go's rule that a
// zero value works or says it does not: each write answers ErrEmptyPath
// before and after Close, which answers nil twice, the Sealer answers
// ErrNoKey from every call, and nothing appears in the working directory,
// where a path of "" would land.
func TestZeroKeyFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	var f KeyFile
	writes := func(when string) {
		for name, write := range map[string]func(context.Context) error{"Create": f.Create, "Rotate": f.Rotate, "Retire": f.Retire} {
			if err := write(t.Context()); !errors.Is(err, filesafe.ErrEmptyPath) {
				t.Errorf("%s %s = %v, want filesafe.ErrEmptyPath", name, when, err)
			}
		}
	}
	writes("before Close")
	for _, name := range []string{"first Close", "second Close"} {
		if err := f.Close(); err != nil {
			t.Errorf("%s = %v, want nil", name, err)
		}
	}
	writes("after Close")
	checkNoKey(t, f.Sealer())
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		t.Errorf("the zero KeyFile made %s", e.Name())
	}
}

// TestOpenKeyFilePath checks that OpenKeyFile refuses, before anything else
// on every system, a path filesafe.ValidatePath refuses (its godoc: empty
// is ErrEmptyPath; relative or holding a NUL is ErrUnsafePath), answering
// no KeyFile.
func TestOpenKeyFilePath(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		want       error
	}{
		{"empty", "", filesafe.ErrEmptyPath},
		{"relative", "sealing.key", filesafe.ErrUnsafePath},
		{"NUL", string(os.PathSeparator) + "sealing\x00.key", filesafe.ErrUnsafePath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := OpenKeyFile(t.Context(), tc.path)
			if f != nil || !errors.Is(err, tc.want) {
				t.Errorf("OpenKeyFile(%q) = a KeyFile %v, %v; want none, %v", tc.path, f != nil, err, tc.want)
			}
		})
	}
}
