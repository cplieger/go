package sealed

import "io/fs"

// platformRefusal says why OpenKeyFile refuses this system: access here is
// an ACL, and filesafe answers ErrModeUnsupported for every mode it would
// set or check.
const platformRefusal = "Windows has no file owner or mode filesafe can check"

// ownerModeErr is never reached: OpenKeyFile refuses this system first.
func ownerModeErr(path string, _ fs.FileInfo) error { return unsupportedErr(path) }
