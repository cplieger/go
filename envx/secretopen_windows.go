//go:build windows

package envx

// openFlagNonblock is zero on Windows, whose filesystems hold no FIFO for an
// open to wait on; the regular-file check still refuses a directory or device.
const openFlagNonblock = 0
