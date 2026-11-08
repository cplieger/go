//go:build !(js || wasip1)

package health

import "syscall"

// oNonblock makes a write-only open of a FIFO with no reader fail with ENXIO
// instead of blocking. Windows defines the flag and ignores it.
const oNonblock = syscall.O_NONBLOCK
