//go:build !unix

package envx

// openFlagNonblock is zero where there are no POSIX FIFOs to wait on
// (Windows, js/wasm, wasip1): the open takes no flag, and the regular-file
// check still refuses a directory or device.
const openFlagNonblock = 0
