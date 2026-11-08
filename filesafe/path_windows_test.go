package filesafe

import (
	"context"
	"errors"
	"os"
	"testing"
)

// TestValidatePath_WindowsForms pins ValidatePath against Windows path
// syntax: a drive-letter or UNC path is absolute, while a rooted (\x) or
// drive-relative (C:x) path depends on the process's current drive or
// directory and is refused (https://learn.microsoft.com/windows/win32/fileio/naming-a-file#fully-qualified-vs-relative-paths).
func TestValidatePath_WindowsForms(t *testing.T) {
	t.Parallel()
	accepted := map[string]string{
		`C:\x\file`:             `C:\x\file`,
		`C:/x/../y`:             `C:\y`,
		`\\server\share\x\file`: `\\server\share\x\file`,
	}
	for in, want := range accepted {
		got, err := validateAbsClean(in)
		if err != nil {
			t.Errorf("validateAbsClean(%q) = %v, want nil", in, err)
			continue
		}
		if got != want {
			t.Errorf("validateAbsClean(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{`\x\file`, `/x/file`, `C:x`, `x\file`} {
		if err := ValidatePath(in); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("ValidatePath(%q) = %v, want ErrUnsafePath", in, err)
		}
	}
}

// TestRootNames_RefuseWindowsAbsoluteForms pins that an in-root entry point
// refuses, with ErrUnsafePath, every name that is not relative to the root:
// absolute, UNC, rooted and drive-relative forms alike.
func TestRootNames_RefuseWindowsAbsoluteForms(t *testing.T) {
	t.Parallel()
	calls := map[string]func(ctx context.Context, root *os.Root, name string) error{
		"WriteFileInRoot": func(ctx context.Context, root *os.Root, name string) error {
			_, err := WriteFileInRoot(ctx, root, name, []byte("x"))
			return err
		},
		"OpenRegularInRoot": func(_ context.Context, root *os.Root, name string) error {
			_, _, err := OpenRegularInRoot(root, name)
			return err
		},
		"RemoveFileInRoot": func(_ context.Context, root *os.Root, name string) error {
			return RemoveFileInRoot(root, name)
		},
		"ProbeWritableInRoot": func(ctx context.Context, root *os.Root, name string) error {
			_, err := ProbeWritableInRoot(ctx, root, name)
			return err
		},
	}
	names := []string{`\etc\passwd`, `/etc/passwd`, `C:x`, `C:\x`, `\\server\share\x`}
	for callName, call := range calls {
		t.Run(callName, func(t *testing.T) {
			t.Parallel()
			root, _ := openTestRoot(t)
			for _, name := range names {
				if err := call(t.Context(), root, name); !errors.Is(err, ErrUnsafePath) {
					t.Errorf("%s(%q) = %v, want ErrUnsafePath", callName, name, err)
				}
			}
		})
	}
}
