//go:build unix || wasip1

package health

import "syscall"

// oNoFollow makes an open of a symlink at the marker path fail instead of
// writing the file it names.
const oNoFollow = syscall.O_NOFOLLOW
