//go:build !(darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || windows)

package sealed

import "io/fs"

// platformRefusal says why OpenKeyFile refuses this system: package syscall
// has no Flock on AIX or Solaris, so filesafe cannot take a lease there.
const platformRefusal = "this system has no flock(2) for the key file's lease"

// ownerModeErr is never reached: OpenKeyFile refuses this system first.
func ownerModeErr(path string, _ fs.FileInfo) error { return unsupportedErr(path) }
