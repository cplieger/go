//go:build !windows

package filesafe

import (
	"context"
	"io/fs"
	"os"
)

// closeTempBeforeRename closes the synced temp while the old target is still
// in place, so a close failure fails the write as PhaseTempClose.
func closeTempBeforeRename(tmp *os.File) error {
	if err := tmp.Close(); err != nil {
		return &WriteError{Phase: PhaseTempClose, Err: err}
	}
	return nil
}

// commitTempInRoot renames the synced, closed temp to name inside root, then
// fsyncs dir. A failed rename removes the temp and returns a PhaseRename
// WriteError. Once the rename succeeds the data is at name; a failed
// directory fsync is logged at Warn and gives durable=false with a nil error.
func commitTempInRoot(_ context.Context, root *os.Root, _ *os.File, tmpName, name, dir string, c *cfg) (durable bool, err error) {
	if rnErr := root.Rename(tmpName, name); rnErr != nil {
		removeTempInRoot(root, tmpName, c.logger)
		return false, &WriteError{Phase: PhaseRename, Err: rnErr}
	}
	if syncErr := fsyncRootDir(root, dir); syncErr != nil {
		c.logger.Warn("filesafe: parent-directory fsync failed; write is not durable",
			"root", root.Name(), "path", name, "error", syncErr)
		return false, nil
	}
	return true, nil
}

// refuseReadOnlyTarget accepts every mode: rename(2) replaces a target
// whatever its permission bits.
func refuseReadOnlyTarget(string, fs.FileMode) error { return nil }
