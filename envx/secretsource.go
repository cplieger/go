package envx

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cplieger/go/masked"
)

// ErrBlankSecretFile is returned by Secret and SecretWithSource when KEY_FILE
// names a readable file that is empty or whitespace only: a broken mount, not a
// secret. It is not a *MissingError, so a caller's default never hides a mount
// the operator did configure.
var ErrBlankSecretFile = errors.New("envx: secret file is blank")

// IsBlankSecretFilePath reports whether KEY_FILE is present but empty or
// whitespace only, and so names no file. It changes nothing: an empty KEY_FILE
// still falls through to KEY and a whitespace-only one still fails to open. Ask
// before resolving an optional secret, where falling through to an unset KEY
// can leave a gate open:
//
//	if envx.IsBlankSecretFilePath("BEAT_TOKEN") { /* refuse to start */ }
func IsBlankSecretFilePath(key Key) bool {
	key.validate()
	path, ok := os.LookupEnv(string(secretFileKey(key)))
	return ok && strings.TrimSpace(path) == ""
}

// secretFileKey names key's companion path variable, for the resolver and
// IsBlankSecretFilePath alike.
func secretFileKey(key Key) Key {
	return key + "_FILE"
}

// SecretSource reports which channel a secret value came from.
type SecretSource string

// The secret delivery channels SecretWithSource distinguishes.
const (
	// SecretSourceNone means no value was found in either channel.
	SecretSourceNone SecretSource = "none"
	// SecretSourceEnv means the value came from KEY itself.
	SecretSourceEnv SecretSource = "env"
	// SecretSourceFile means the value came from the file named by KEY_FILE.
	SecretSourceFile SecretSource = "file"
)

// SecretWithSource is Secret, also reporting the channel that answered, on the
// error paths too. KEY_FILE wins when it is not empty, so a caller can warn that
// a KEY it also set was ignored:
//
//	v, src, err := envx.SecretWithSource("PFX_PASSWORD")
//	if src == envx.SecretSourceFile && os.Getenv("PFX_PASSWORD") != "" { /* warn */ }
func SecretWithSource(key Key) (value masked.Secret, source SecretSource, err error) {
	key.validate()
	if path := os.Getenv(string(secretFileKey(key))); path != "" {
		data, readErr := readSecretFile(path)
		if readErr != nil {
			return masked.Secret{}, SecretSourceFile, fmt.Errorf("read secret file for %s: %w", key, readErr)
		}
		content := string(data)
		// Blankness is judged on the trimmed content; the value keeps every byte
		// but one trailing line ending.
		if strings.TrimSpace(content) == "" {
			// A blank file cannot be diagnosed without its path.
			return masked.Secret{}, SecretSourceFile, fmt.Errorf("%w for %s: %s", ErrBlankSecretFile, key, path)
		}
		return masked.NewSecret(trimTrailingLineEnding(content)), SecretSourceFile, nil
	}
	v, reqErr := Require(key)
	if reqErr != nil {
		return masked.Secret{}, SecretSourceNone, reqErr
	}
	return masked.NewSecret(v), SecretSourceEnv, nil
}

// trimTrailingLineEnding removes at most one trailing "\n" or "\r\n", the one
// an editor or `kubectl create secret --from-file` appends. Every other byte is
// the credential, so the file channel answers what KEY would for the same
// value; a value ending in a real newline cannot be stored this way.
func trimTrailingLineEnding(s string) string {
	if !strings.HasSuffix(s, "\n") {
		return s
	}
	return strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r")
}
