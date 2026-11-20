package filesafe

import (
	"errors"
	"fmt"
	"os"
)

var (
	// ErrEmptyPath is returned when a path argument is empty.
	ErrEmptyPath = errors.New("filesafe: empty path")
	// ErrUnsafePath is returned when a path fails the local safety check, and
	// when a function is given a nil *os.Root or *os.File: every function
	// taking one refuses nil with an error matching it.
	ErrUnsafePath = errors.New("filesafe: unsafe path")
	// ErrInvalidArgument is returned for an argument no call could mean: a
	// non-positive sweep maxAge, a negative read maxBytes, WithMode(0) or
	// WithMkdirMode(0), and a nil reader or walk function. Nothing is created
	// or removed first. Match with errors.Is.
	ErrInvalidArgument = errors.New("filesafe: invalid argument")
	// ErrFileTooLarge is returned when a file exceeds the ReadBounded /
	// ReadBoundedFile size limit, or content exceeds a WithMaxBytes write cap
	// (there it may arrive wrapped, sometimes inside a *WriteError; match with
	// errors.Is). For the sizes, errors.As a *FileTooLargeError.
	ErrFileTooLarge = errors.New("filesafe: file too large")
	// ErrSymlinkTarget is returned when the target path is a symlink, which
	// every write entry point, OpenRegularNoFollow, OpenRegularInRootNoFollow,
	// EnsurePrivateDir, the CheckPrivateDir pair and the lease functions refuse,
	// and by OpenParentInRoot (so RemoveFileInRoot) for a symlinked ancestor.
	ErrSymlinkTarget = errors.New("filesafe: target is a symlink")
	// ErrRaced is returned when a name changed between a check and the open
	// after it: by OpenRegularInRootNoFollow, CheckPrivateDirInRoot, the lease
	// functions and OpenParentInRoot (so RemoveFileInRoot). A retry is the
	// sane response, unlike ErrSymlinkTarget, a permanent verdict about what is
	// at the name. Match with errors.Is.
	ErrRaced = errors.New("filesafe: path changed between the check and the open")
	// ErrNotRegular is returned when a name resolves to something other than a
	// regular file, which ReadBounded, ReadBoundedInRoot, the OpenRegular
	// functions, RemoveFileInRoot, every write entry point and the lease
	// functions refuse. Match with errors.Is; for the mode, errors.As a
	// *NotRegularError.
	ErrNotRegular = errors.New("filesafe: not a regular file")
	// ErrNotDirectory is returned by EnsurePrivateDir and the CheckPrivateDir
	// pair when the name is occupied by something other than a directory, and
	// by OpenParentInRoot for an ancestor of the name that is neither a
	// directory nor a symlink. Match with errors.Is; for the mode, errors.As
	// a *NotDirectoryError.
	ErrNotDirectory = errors.New("filesafe: not a directory")
	// ErrNotOwned is returned by EnsurePrivateDir, the CheckPrivateDir pair and
	// the lease functions when the directory's or lock file's owner is not the
	// effective uid or could not be determined, wrapping the permission error
	// when that owner's mode refused the lookup or open. Match with errors.Is;
	// for the uids, errors.As a *NotOwnedError.
	ErrNotOwned = errors.New("filesafe: not owned by the effective uid")
	// ErrModeTooOpen is returned when a pre-existing directory or lock file
	// grants group or other access: by EnsurePrivateDir unless
	// WithRepairOwnedDir repairs it, and by the CheckPrivateDir pair and the
	// lease functions, which never repair. Match with errors.Is; for the mode,
	// errors.As a *ModeTooOpenError.
	ErrModeTooOpen = errors.New("filesafe: mode grants group or other access")
	// ErrLeaseHeld is returned by TryLease and TrySharedLease while another
	// holder has the lease, and by AcquireLease and AcquireSharedLease when ctx
	// ended while it did, wrapping the context's error. Match with errors.Is.
	ErrLeaseHeld = errors.New("filesafe: lease held by another holder")
	// ErrModeNotStored is returned when the mode read back after a chmod does
	// not match what was requested, a mount property rather than a permission
	// error: by EnforceMode, a write or probe under WithMode or WithMkdirMode,
	// EnsurePrivateDir and a lease creating its lock file. Match with
	// errors.Is; for both modes, errors.As a *ModeNotStoredError.
	ErrModeNotStored = errors.New("filesafe: filesystem did not store the requested mode")
	// ErrParentNotSynced is returned by EnsurePrivateDir when dir's parent
	// could not be fsynced; unlike its other errors, a retry is safe
	// (PrivateDirResult). Match with errors.Is.
	ErrParentNotSynced = errors.New("filesafe: parent directory not synced")
	// ErrAborted is returned by PendingFile.Commit when the pending file was
	// already aborted by a prior Cleanup or Close.
	ErrAborted = errors.New("filesafe: pending file aborted")
	// ErrFileBusy is returned on Windows when the replace was refused through
	// every retry, or the context ended while waiting to retry. It arrives
	// inside a *WriteError with PhaseRename, wrapping the last refusal's
	// syscall.Errno (and the context error, if any): ERROR_SHARING_VIOLATION
	// or ERROR_LOCK_VIOLATION means another handle held the file, while
	// ERROR_ACCESS_DENIED is either such a handle or an ACL refusal, which
	// Windows reports the same way. Match with errors.Is.
	ErrFileBusy = errors.New("filesafe: target in use or access denied")
	// ErrModeUnsupported is returned on Windows by EnforceMode,
	// EnsurePrivateDir, the CheckPrivateDir pair, the lease functions and any
	// write or probe given a nonzero WithMode or WithMkdirMode, before anything
	// is created: Go's chmod there only sets the read-only attribute. A zero
	// mode is ErrInvalidArgument on every platform. Match with errors.Is.
	ErrModeUnsupported = errors.New("filesafe: file modes are not supported on this platform")
)

// WritePhase identifies which step of an atomic write failed. Each value
// appears only on a WriteError. A parent-directory fsync failure is not in
// this enum; it is surfaced via Result.Durable instead.
type WritePhase int

const (
	// PhaseTempCreate indicates failure opening the destination for writing:
	// opening the target's parent directory (the absolute-path entry points
	// open it as an *os.Root), creating the temp file inside it, or, under
	// WithMode, proving the new temp owner-only before any data is written.
	PhaseTempCreate WritePhase = iota + 1
	// PhaseTempWrite indicates failure writing to the temp file.
	PhaseTempWrite
	// PhaseTempChmod indicates failure enforcing WithMode's final mode on the
	// written temp file; it occurs only under WithMode.
	PhaseTempChmod
	// PhaseTempSync indicates failure syncing the temp file.
	PhaseTempSync
	// PhaseTempClose indicates failure closing the temp file.
	PhaseTempClose
	// PhaseRename indicates failure renaming temp to the final path.
	PhaseRename
)

// String names the phase in the words a WriteError's message uses.
func (p WritePhase) String() string {
	switch p {
	case PhaseTempCreate:
		return "create temp file"
	case PhaseTempWrite:
		return "write temp file"
	case PhaseTempChmod:
		return "chmod temp file"
	case PhaseTempSync:
		return "sync temp file"
	case PhaseTempClose:
		return "close temp file"
	case PhaseRename:
		return "rename to final path"
	default:
		return "unknown phase"
	}
}

// WriteError wraps a failure inside an atomic write, once its temp file is
// being created, with the phase that failed. A WriteError always means the
// data did NOT reach its final path. A refusal before that point (an invalid
// argument or option, a done context, an over-cap size, a target that is a
// symlink or not a regular file, a parent directory that could not be
// created) is a plain error matching its sentinel instead.
type WriteError struct {
	// Err is the failure, matched through Unwrap.
	Err error
	// Phase is the step of the write that failed.
	Phase WritePhase
}

// Error renders "filesafe: <phase>: <cause>". A nil Err renders as "no
// cause", so a WriteError built by hand never panics.
func (e *WriteError) Error() string {
	if e.Err == nil {
		return "filesafe: " + e.Phase.String() + ": no cause"
	}
	return "filesafe: " + e.Phase.String() + ": " + e.Err.Error()
}

// Unwrap returns Err.
func (e *WriteError) Unwrap() error { return e.Err }

// NotRegularError reports that a name resolved to something other than a
// regular file, and carries the mode that decided it.
//
// The mode is a DIAGNOSTIC a caller reports, and reading it out of a message
// string is not something a caller should have to do. Every producer here
// returns one, so a caller resolving a name once never has to stat the path a
// second time just to describe a refusal. It unwraps to ErrNotRegular, so
// errors.Is keeps matching for callers that only need the verdict.
type NotRegularError struct {
	// Name is the path or root-relative name as the caller supplied it, cleaned.
	Name string
	// Mode is the full FileMode of what was actually there. Type() names the
	// kind; the permission bits are kept because a caller reporting an
	// unexpected object usually wants both.
	Mode os.FileMode
}

// Error renders the verdict, the name and the kind of file found.
func (e *NotRegularError) Error() string {
	return fmt.Sprintf("%s: %s (type %s)", ErrNotRegular.Error(), e.Name, e.Mode.Type())
}

// Unwrap returns ErrNotRegular, so errors.Is matches the verdict.
func (*NotRegularError) Unwrap() error { return ErrNotRegular }

// notRegular is the one producer, so the refusal sites cannot drift in
// what they report.
func notRegular(name string, mode os.FileMode) error {
	return &NotRegularError{Name: name, Mode: mode}
}

// ModeTooOpenError reports that a directory or lock file grants group or
// other access, and carries the mode that decided it, so a caller describing
// the refusal never stats the path again. It unwraps to ErrModeTooOpen.
type ModeTooOpenError struct {
	// Name is the path or root-relative name as the caller supplied it, cleaned.
	Name string
	// Mode is what the check read from the open handle: the permission bits
	// plus setuid, setgid and sticky.
	Mode os.FileMode
}

// Error renders the verdict, the name and the mode found, in chmod(1) octal.
func (e *ModeTooOpenError) Error() string {
	return fmt.Sprintf("%s: %s (mode %s)", ErrModeTooOpen.Error(), e.Name, octalMode(e.Mode))
}

// Unwrap returns ErrModeTooOpen, so errors.Is matches the verdict.
func (*ModeTooOpenError) Unwrap() error { return ErrModeTooOpen }

// NotDirectoryError reports that a name resolved to something other than a
// directory, and carries the mode that decided it. It unwraps to
// ErrNotDirectory.
type NotDirectoryError struct {
	// Name is the path, root-relative name or path component checked, cleaned.
	Name string
	// Mode is the full FileMode of what was there. Type() names a special
	// file's kind and is 0 for a regular file, which has no type bits.
	Mode os.FileMode
}

// Error renders the verdict, the name and the kind of file found.
func (e *NotDirectoryError) Error() string {
	kind := e.Mode.Type().String()
	if e.Mode.IsRegular() {
		kind = "regular file"
	}
	return fmt.Sprintf("%s: %s (type %s)", ErrNotDirectory.Error(), e.Name, kind)
}

// Unwrap returns ErrNotDirectory, so errors.Is matches the verdict.
func (*NotDirectoryError) Unwrap() error { return ErrNotDirectory }

// notDirectory is the one producer, so the refusal sites cannot drift in
// what they report.
func notDirectory(name string, mode os.FileMode) error {
	return &NotDirectoryError{Name: name, Mode: mode}
}

// NotOwnedError reports that a directory or lock file is not owned by the
// effective uid, and carries both uids the check compared. It unwraps to
// ErrNotOwned.
type NotOwnedError struct {
	// Name is the path or root-relative name checked, cleaned, as the caller
	// supplied it. When a lookup of a root's own directory is refused, it is
	// the path the root was opened at, the path its owner was read from.
	Name string
	// Owner is the uid that owns Name, or -1 when it could not be determined.
	// A uid is never negative, on 32-bit platforms too.
	Owner int64
	// EUID is the effective uid Owner was compared with.
	EUID int64
}

// Error renders the verdict, the name and both uids.
func (e *NotOwnedError) Error() string {
	if e.Owner < 0 {
		return fmt.Sprintf("%s: %s: ownership could not be determined", ErrNotOwned.Error(), e.Name)
	}
	return fmt.Sprintf("%s: %s: owned by uid %d, want the effective uid %d; its owner could replace the checked path",
		ErrNotOwned.Error(), e.Name, e.Owner, e.EUID)
}

// Unwrap returns ErrNotOwned, so errors.Is matches the verdict.
func (*NotOwnedError) Unwrap() error { return ErrNotOwned }

// ModeNotStoredError reports that the mode read back after a chmod is not
// the one requested, and carries both. It unwraps to ErrModeNotStored.
type ModeNotStoredError struct {
	// Name is the file or directory as it was opened, cleaned.
	Name string
	// Requested and Stored hold the permission bits plus setuid, setgid and
	// sticky: what was asked for, and what the filesystem kept.
	Requested, Stored os.FileMode
}

// Error renders the verdict, the name and both modes in chmod(1) octal.
func (e *ModeNotStoredError) Error() string {
	return fmt.Sprintf("%s: %s: asked for %s, filesystem stored %s",
		ErrModeNotStored.Error(), e.Name, octalMode(e.Requested), octalMode(e.Stored))
}

// Unwrap returns ErrModeNotStored, so errors.Is matches the verdict.
func (*ModeNotStoredError) Unwrap() error { return ErrModeNotStored }

// FileTooLargeError reports content over a size cap, and carries the cap and
// the size that crossed it. It unwraps to ErrFileTooLarge.
type FileTooLargeError struct {
	// Size is how large the content was found or would have grown: the
	// stat size of a file read or committed, the size a rejected write or
	// truncate would have reached, or Limit+1 for a file that grew past
	// Limit during a read. A file read or committed holds at least Size
	// bytes; nothing holds the size a rejected write or truncate asked for.
	Size int64
	// Limit is the cap: ReadBounded's maxBytes or WithMaxBytes.
	Limit int64
}

// Error renders the verdict and both sizes.
func (e *FileTooLargeError) Error() string {
	return fmt.Sprintf("%s: %d bytes (max %d)", ErrFileTooLarge.Error(), e.Size, e.Limit)
}

// Unwrap returns ErrFileTooLarge, so errors.Is matches the verdict.
func (*FileTooLargeError) Unwrap() error { return ErrFileTooLarge }
