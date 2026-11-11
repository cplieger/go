//go:build !(unix || wasip1)

package health

// oNoFollow is 0 on windows, js and plan9, where package syscall defines no
// O_NOFOLLOW; markerWritable's Lstat still refuses a symlink at NewStatus.
const oNoFollow = 0
