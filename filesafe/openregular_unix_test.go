//go:build !windows

package filesafe

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
	"testing"
)

// TestNoFollowRefusal_ALinkIsErrSymlinkTargetWhateverTheErrno pins that a
// final symlink an O_NOFOLLOW open refused is ErrSymlinkTarget under each
// kernel's errno: ELOOP on Linux, macOS, OpenBSD and illumos, EMLINK on
// FreeBSD and DragonFly (open(2)), EINVAL on DragonFly (Go's os.isNoFollowErr).
// An ELOOP with nothing to lstat is an ancestor loop and takes the same arm.
func TestNoFollowRefusal_ALinkIsErrSymlinkTargetWhateverTheErrno(t *testing.T) {
	t.Parallel()
	link := found(lstatOccupants(t).symlink)
	gone := func() (os.FileInfo, error) { return nil, fs.ErrNotExist }
	for name, tc := range map[string]struct {
		lstat func() (os.FileInfo, error)
		errno syscall.Errno
	}{
		"ELOOP":              {link, syscall.ELOOP},
		"EMLINK":             {link, syscall.EMLINK},
		"EINVAL":             {link, syscall.EINVAL},
		"ELOOP, lstat fails": {gone, syscall.ELOOP},
	} {
		got := noFollowRefusal(&fs.PathError{Op: "open", Path: "l", Err: tc.errno}, "l", tc.lstat)
		if !errors.Is(got, ErrSymlinkTarget) || !errors.Is(got, tc.errno) {
			t.Errorf("%s: noFollowRefusal = %v, want ErrSymlinkTarget wrapping %v", name, got, tc.errno)
		}
	}
}

// TestNoFollowRefusal_OtherOccupantsAreSocketRefusals pins the arms past the
// link check: a socket is a *NotRegularError naming it, and an errno on a
// regular file comes back unchanged.
func TestNoFollowRefusal_OtherOccupantsAreSocketRefusals(t *testing.T) {
	t.Parallel()
	occ := lstatOccupants(t)
	got := noFollowRefusal(&fs.PathError{Op: "open", Path: "s", Err: syscall.EOPNOTSUPP}, "s", found(occ.socket))
	var nre *NotRegularError
	if !errors.As(got, &nre) || nre.Mode&os.ModeSocket == 0 {
		t.Errorf("noFollowRefusal(EOPNOTSUPP on a socket) = %v, want a *NotRegularError naming a socket", got)
	}
	openErr := &fs.PathError{Op: "open", Path: "f", Err: syscall.EACCES}
	if got := noFollowRefusal(openErr, "f", found(occ.regular)); got != openErr { //nolint:errorlint // identity: the same error comes back
		t.Errorf("noFollowRefusal(EACCES on a file) = %v, want the open's error unchanged", got)
	}
}
