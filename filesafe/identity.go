package filesafe

import (
	"os"
	"time"
)

// FileIdentity records which generation of a file a reader holds, so a cache
// can tell from a stat alone whether its copy is still current. Matches needs
// both an equal mtime and os.SameFile: an in-place write can keep the mtime,
// and a publish by rename can carry an old one. It does not compare size; a
// caller whose file is also rewritten in place keeps its own size check
// (docs/reload-staleness.md). The zero value records nothing and reports
// Changed. What to do about a change stays with the caller.
type FileIdentity struct {
	info os.FileInfo
}

// Identify captures the identity of the file described by info, as returned by
// os.Stat, os.Lstat, or (*os.File).Stat. A nil info records nothing, so the
// result reports Changed against every later stat. On Windows it records the
// file at the path when Identify runs, so call it right after the stat.
func Identify(info os.FileInfo) FileIdentity {
	if info != nil {
		// On Windows an os.Stat result reads its file ID by path at its first
		// os.SameFile (os/types_windows.go loadFileId), which would identify
		// whatever file a later Matches finds there. Comparing it with itself
		// loads the ID now.
		_ = os.SameFile(info, info)
	}
	return FileIdentity{info: info}
}

// Recorded reports whether an identity was captured at all. A zero FileIdentity
// (nothing loaded yet, or a load that failed before its stat) records nothing.
func (id FileIdentity) Recorded() bool { return id.info != nil }

// Matches reports whether info describes the same file generation this identity
// captured: an equal modification time AND the same file (os.SameFile, which
// compares device and inode, or volume and file index on Windows). An unrecorded identity matches
// nothing, and a nil info (no file observed) matches nothing.
func (id FileIdentity) Matches(info os.FileInfo) bool {
	if id.info == nil || info == nil {
		return false
	}
	return info.ModTime().Equal(id.info.ModTime()) && os.SameFile(info, id.info)
}

// Changed is the negation of Matches, for the reload-side reading: it reports
// whether info is a file generation this identity did not capture, and so
// whether a reader holding the captured generation must load again.
func (id FileIdentity) Changed(info os.FileInfo) bool { return !id.Matches(info) }

// ModTime returns the captured modification time, or the zero Time when
// nothing was recorded. For diagnostics only; Matches owns the freshness
// test, since mtime alone is not sufficient.
func (id FileIdentity) ModTime() time.Time {
	if id.info == nil {
		return time.Time{}
	}
	return id.info.ModTime()
}
