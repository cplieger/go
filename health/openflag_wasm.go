//go:build js || wasip1

package health

// oNonblock is 0 on js and wasip1, where package syscall defines no
// O_NONBLOCK.
const oNonblock = 0
