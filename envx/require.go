package envx

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cplieger/go/masked"
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

// maxSecretFileSize caps a KEY_FILE read. Real secrets are tens of bytes; the
// cap guards against a variable pointing at a runaway log.
const maxSecretFileSize = 1 << 20

// The failure classes of a KEY_FILE read, matched with errors.Is. Every error
// Secret and SecretWithSource return for a configured file wraps exactly one of
// them or ErrBlankSecretFile, so a caller can say why the file failed without
// matching text or echoing the path, which holds the secret itself when
// KEY_FILE was set to the value by mistake.
var (
	// ErrSecretFilePathRejected means KEY_FILE was not clean or had a ".."
	// component.
	ErrSecretFilePathRejected = errors.New("envx: secret file path rejected")
	// ErrSecretFileTooLarge means the file was over 1 MiB when opened.
	ErrSecretFileTooLarge = errors.New("envx: secret file exceeds the size limit")
	// ErrSecretFileGrew means the file grew past 1 MiB during the read, which
	// would otherwise have returned it cut short.
	ErrSecretFileGrew = errors.New("envx: secret file grew past the size limit during read")
	// ErrSecretFileUnreadable means the system refused the open, stat or read,
	// or the path named a FIFO, a directory or a device. The *os.PathError
	// stays reachable with errors.As; for a non-regular file its Err reads
	// "not a regular file (mode ...)".
	ErrSecretFileUnreadable = errors.New("envx: secret file could not be read")
)

// classifiedError carries a failure class beside its message instead of inside
// it, so messages that interpolate values mid-sentence keep their exact text.
// Unwrap answers the class and the cause, so errors.Is finds the class and
// errors.As still finds an *os.PathError.
type classifiedError struct {
	class error
	cause error // nil when there is no underlying error
	msg   string
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

// unreadableSecretFile classifies an OS refusal, keeping its message as is.
func unreadableSecretFile(cause error) error {
	return &classifiedError{msg: cause.Error(), class: ErrSecretFileUnreadable, cause: cause}
}

// Secret returns a required secret as a [masked.Secret], which no printer shows;
// call Reveal where the value is sent. When KEY_FILE is set and not empty, the
// secret is that file's content with at most one trailing line ending removed,
// read through one handle and capped at 1 MiB; otherwise it is KEY as set. An
// unset or empty result is a *MissingError and a blank file ErrBlankSecretFile.
// Errors carry the key, never the value. Secret is SecretWithSource without the
// channel.
func Secret(key Key) (masked.Secret, error) {
	v, _, err := SecretWithSource(key)
	return v, err
}

// secretStatHook, when set by a test, runs after the Stat and before the size
// gate and the read: the window in which a writer can grow the file.
var secretStatHook func()

// readSecretFile reads a secret file through one handle, so the path cannot be
// swapped between the check and the read. The path is judged as written:
// IsCanonical refuses any spelling Clean would rewrite and HasDotDot a ".."
// component, while two dots inside a name, as in key..v2, pass. The checked
// and the opened bytes are therefore the same, and Clean below is a no-op
// backstop that gosec does not count as a G703 sanitizer, hence the nosec.
func readSecretFile(path string) ([]byte, error) {
	if !pathinside.IsCanonical(path) || pathinside.HasDotDot(path) {
		return nil, fmt.Errorf("%w (must be clean and contain no \"..\" path component): %s", ErrSecretFilePathRejected, path)
	}
	// Non-blocking, so a FIFO with no writer cannot hang the open; symlinks are
	// followed, so a Kubernetes secret's ..data link still reaches its file.
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDONLY|openFlagNonblock, 0) // #nosec G703 -- the guard above refuses a non-canonical path or a ".." component, so no traversal is reachable
	if err != nil {
		return nil, unreadableSecretFile(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, unreadableSecretFile(err)
	}
	if secretStatHook != nil {
		secretStatHook()
	}
	if !info.Mode().IsRegular() {
		// Classified like an OS refusal, so the message names the path as
		// "open <path>: not a regular file (mode ...)" does.
		return nil, unreadableSecretFile(&fs.PathError{
			Op: "open", Path: path, Err: fmt.Errorf("not a regular file (mode %s)", info.Mode()),
		})
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
