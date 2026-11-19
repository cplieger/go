//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package sealed

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// platformRefusal is empty: these systems have file owners, modes and the
// flock(2) filesafe's lease takes.
const platformRefusal = ""

// geteuid is a seam: an unprivileged test cannot make another user's file.
var geteuid = os.Geteuid

// ownerModeErr refuses a key file whose open descriptor's fstat, fi, shows
// another owner than the effective uid, or a mode bit outside 0600: group
// or other access, owner execute, setuid, setgid or sticky (inode(7)).
func ownerModeErr(path string, fi fs.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: %s: its owner could not be read", ErrUnsafeKeyFile, path)
	}
	if euid := geteuid(); int(st.Uid) != euid {
		return fmt.Errorf("%w: %s: owned by uid %d, not the effective uid %d", ErrUnsafeKeyFile, path, st.Uid, euid)
	}
	if perm := st.Mode & 0o7777; perm&^0o600 != 0 {
		return fmt.Errorf("%w: %s: mode %#o, want 0600 or narrower", ErrUnsafeKeyFile, path, perm)
	}
	return nil
}
