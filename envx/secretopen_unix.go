//go:build !windows

package envx

import "syscall"

// openFlagNonblock makes the secret file's open return at once on a FIFO with
// no writer, where a plain open(2) would wait forever; the regular-file check
// after it then refuses the FIFO. It has no effect on a regular file's reads.
const openFlagNonblock = syscall.O_NONBLOCK
