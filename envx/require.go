package envx

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/cplieger/go/pathinside"
)

// MissingError reports a required environment variable that is unset or
// empty. It carries the key so a caller can aggregate several missing
// variables into one startup failure.
type MissingError struct {
	// Key is the environment variable name that was required.
	Key Key
}

// Error implements the error interface.
func (e *MissingError) Error() string {
	return "required environment variable is missing: " + string(e.Key)
}

// Require returns the value of the environment variable key, or a
// *MissingError when it is unset or empty. It returns an error rather than
// exiting so the caller controls startup failure (collect every missing key,
// log through the configured handler, then exit once).
func Require(key Key) (string, error) {
	return Source{}.Require(key)
}

// maxSecretFileSize bounds a KEY_FILE secret read. Real secrets are tens of
// bytes; the 1 MB ceiling only guards against pointing the variable at a
// device file or a runaway log.
const maxSecretFileSize = 1 << 20

// The failure classes of a KEY_FILE secret read. Every error Secret and
// SecretWithSource return for a configured secret file wraps exactly one of
// these or ErrBlankSecretFile, so a caller can name WHY the file was unusable
// with errors.Is and apply a different policy per class.
//
// The classes exist because the alternative was worse: without them the only
// way to tell a rejected path from an oversized file was matching this
// package's error TEXT, so callers folded every non-blank failure into one
// generic message instead. That mattered here more than in most packages,
// because these errors embed the operator-supplied PATH and a caller whose
// KEY_FILE was misconfigured to hold the secret ITSELF (rather than a path to
// it) must be able to describe the failure without echoing the value into a
// log. Naming the class no longer requires touching the path.
//
// The classes are deliberately narrow rather than one "unusable file" sentinel:
// their remediations differ (fix the variable, shrink the file, stop rewriting
// it, fix the mount) and only a caller writing an operator-facing message knows
// which of those it wants to say.
var (
	// ErrSecretFilePathRejected means KEY_FILE named a path this package
	// refuses to open: not already clean, or containing a ".." path component.
	ErrSecretFilePathRejected = errors.New("envx: secret file path rejected")
	// ErrSecretFileTooLarge means the file was already over maxSecretFileSize
	// when it was opened.
	ErrSecretFileTooLarge = errors.New("envx: secret file exceeds the size limit")
	// ErrSecretFileGrew means the file passed the size gate and then grew past
	// maxSecretFileSize while it was being read, so the content would have been
	// silently truncated.
	ErrSecretFileGrew = errors.New("envx: secret file grew past the size limit during read")
	// ErrSecretFileUnreadable means the operating system refused the open, stat
	// or read. The underlying *os.PathError stays reachable with errors.As, so
	// a caller can still report the syscall and its reason (without the path,
	// which the PathError also carries).
	ErrSecretFileUnreadable = errors.New("envx: secret file could not be read")
)

// classifiedError attaches a failure class to an error message without altering
// the message.
//
// Every class above is reached with errors.Is, and where wrapping the sentinel
// with fmt.Errorf("%w …") reproduces the existing message byte for byte that is
// what readSecretFile does. The remaining messages predate the sentinels and
// interpolate values in the middle of the sentence ("secret file is 5 bytes,
// exceeds …"), so splicing a sentinel into them would rewrite text that
// consumers and this package's own tests read. This carries the class beside
// the message instead of inside it.
//
// Unwrap returns a slice so the OS-failure class can hold both its sentinel and
// the *os.PathError underneath: errors.Is finds the class, errors.As still
// finds the PathError.
type classifiedError struct {
	class error
	cause error  // nil unless there is an underlying error to preserve
	msg   string // the message exactly as it read before the class existed
}

// Error implements the error interface, returning the message unchanged.
func (e *classifiedError) Error() string { return e.msg }

// Unwrap reports the failure class and, when there is one, the underlying cause.
func (e *classifiedError) Unwrap() []error {
	if e.cause == nil {
		return []error{e.class}
	}
	return []error{e.class, e.cause}
}

// unreadableSecretFile classifies an OS-level open, stat or read failure. The
// message is the operating system's own, unchanged: it is already a
// *os.PathError naming the syscall and the reason.
func unreadableSecretFile(cause error) error {
	return &classifiedError{msg: cause.Error(), class: ErrSecretFileUnreadable, cause: cause}
}

// Secret returns a required secret from the environment, supporting the
// Docker secrets convention: when KEY_FILE is set to a non-empty value, the
// secret is read from that file (size-bounded, with at most one trailing line
// ending removed); otherwise the value of KEY itself is returned. An unset or
// empty result is a *MissingError, and a configured file whose content is
// blank is ErrBlankSecretFile.
//
// The file channel returns the secret as written apart from that single
// trailing newline, so a value whose leading or trailing SPACES are part of the
// credential survives the round trip: the two channels must not disagree about
// what the secret IS, and KEY is never rewritten either. Only the line ending
// an editor or `kubectl create secret` appends is removed, because that one is
// an artifact of storing a value in a file rather than part of the value.
//
// The KEY_FILE indirection keeps the secret value out of `docker inspect`
// output and compose files; the file path must be clean (no ".." traversal),
// and the read uses a single handle so the size check and the read cannot
// race. The secret value itself never appears in an error or a log line;
// errors carry the key name and file path only. Each failure class is named by
// a sentinel (ErrSecretFilePathRejected, ErrSecretFileTooLarge,
// ErrSecretFileGrew, ErrSecretFileUnreadable, ErrBlankSecretFile) so a caller
// can report WHY the file was unusable without echoing the path.
//
// Use SecretWithSource when the caller needs to know WHICH channel supplied the
// value — for example to warn that a KEY it also set was ignored in favour of
// KEY_FILE. Secret delegates to it, so the two cannot drift. Use
// IsBlankSecretFilePath when a KEY_FILE that is present but blank must be
// refused instead of read as absent.
func Secret(key Key) (string, error) {
	v, _, err := SecretWithSource(key)
	return v, err
}

// readSecretFile reads a secret file through one handle (no stat-then-open
// TOCTOU window) and rejects a path containing traversal or a file over the
// size bound.
//
// Every failure it returns is classified: ErrSecretFilePathRejected,
// ErrSecretFileTooLarge, ErrSecretFileGrew or ErrSecretFileUnreadable, so a
// caller can name the class without matching these messages or handling the
// path.
//
// The path rule is pathinside's hygiene pair, judging the path AS WRITTEN
// rather than where it resolves: IsCanonical refuses every spelling a later
// normalization would silently rewrite (a trailing or doubled separator, a "."
// component, a traversal buried mid-path), and HasDotDot refuses a ".." path
// COMPONENT. The pair deliberately does NOT refuse two dots INSIDE a name:
// /run/secrets/key..v2, or a file literally named "...", is an ordinary file,
// and the substring test this rule used to run refused it for nothing — a
// component that is not exactly ".." traverses nowhere. Every path that could
// reach a file other than the one it names is still refused: the traversal
// spellings ("..", "../token", /run/secrets/../../etc/shadow) and every
// unclean spelling, whether or not it carries a traversal.
//
// The canonicality half means the string that is validated and the string that
// is opened are the same bytes: a path that passes IS its own Clean form, so
// the filepath.Clean below is a proven no-op, kept as a backstop in case the
// guard above is ever weakened. It is NOT there to satisfy a taint checker:
// gosec explicitly declines to treat Clean as a G703 sanitizer, because Clean
// RESOLVES a ".." element rather than rejecting it (its analyzers/pathtraversal.go
// names os.Root or a prefix check as the only real confinement). Only
// filepath.Base, filepath.Rel and path.Base clear that analyzer, and none of the
// three can be used here without discarding the directory the operator named.
func readSecretFile(path string) ([]byte, error) {
	if !pathinside.IsCanonical(path) || pathinside.HasDotDot(path) {
		return nil, fmt.Errorf("%w (must be clean and contain no \"..\" path component): %s", ErrSecretFilePathRejected, path)
	}
	f, err := os.Open(filepath.Clean(path)) // #nosec G703 -- the guard above refuses a non-canonical path or a ".." component, so no traversal is reachable
	if err != nil {
		return nil, unreadableSecretFile(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, unreadableSecretFile(err)
	}
	if info.Size() > maxSecretFileSize {
		return nil, &classifiedError{
			msg:   fmt.Sprintf("secret file is %d bytes, exceeds %d byte limit", info.Size(), maxSecretFileSize),
			class: ErrSecretFileTooLarge,
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSecretFileSize+1))
	if err != nil {
		return nil, unreadableSecretFile(err)
	}
	// Re-check after reading: a file that grows between Stat and read would
	// otherwise pass the size gate and return silently truncated content.
	if len(data) > maxSecretFileSize {
		return nil, &classifiedError{
			msg:   fmt.Sprintf("secret file grew past the %d byte limit during read", maxSecretFileSize),
			class: ErrSecretFileGrew,
		}
	}
	return data, nil
}
