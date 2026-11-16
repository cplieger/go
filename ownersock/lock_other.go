//go:build !(darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd)

package ownersock

// lockDir takes no lock: syscall has no Flock on AIX or Solaris, where the
// listener refuses every peer, and Listen fails on Windows before it binds.
func lockDir(string) (unlock func(), err error) { return func() {}, nil }
