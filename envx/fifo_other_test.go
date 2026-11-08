//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly)

package envx

import "errors"

// mkfifoForTest reports that this platform has no FIFO to make.
func mkfifoForTest(string) error { return errors.New("no FIFOs on this platform") }
