// Package filesafe provides crash-safe atomic file writes (temp, fsync,
// rename, then a directory fsync, or on Windows a flush of the published
// file, all through an *os.Root), bounded reads, regular-file opens,
// writability probes and stale-temp sweeps, using only the standard library.
// A nil error from a write means the data reached its final path.
//
// A write without [WithMode] creates the file like [os.Create] (0666 through
// the umask and any inherited ACL) and never chmods or verifies it. WithMode
// stages the payload in an owner-only file and proves the mode the filesystem
// stored on the staging and final file, failing with [ErrModeNotStored]
// instead; on Windows the mode options return [ErrModeUnsupported] for a
// nonzero mode.
// https://github.com/cplieger/go/tree/main/filesafe documents every guarantee.
package filesafe
