package envx

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrBlankSecretFile is returned by Secret and SecretWithSource when KEY_FILE names a
// readable file whose content is blank — empty, or whitespace only.
//
// Blankness is the one judgement made on the whitespace-trimmed content: a file holding
// only spaces or newlines is a broken secret mount, not a secret. The value a readable
// file DOES carry is returned with at most a trailing line ending removed, so the two
// operations do not share a rule (see trimTrailingLineEnding).
//
// It is a distinct sentinel because a caller often has a policy for a blank secret that
// differs from its policy for an unusable one. cert-converter is the motivating case: it
// offers PFX_ALLOW_EMPTY_PASSWORD as an explicit opt-in to running without a password,
// and that opt-in has to mean the same thing whether the blank value arrived through the
// environment or through a mounted file. Without a sentinel the file channel could only
// be classified by matching this package's error TEXT, so the opt-in silently applied to
// one channel and not the other.
//
// It is deliberately NOT a *MissingError: the operator did configure a secret file, and
// treating "you forgot to set this" the same as "the file you mounted is blank" would
// hide a broken secret mount behind a caller's default.
var ErrBlankSecretFile = errors.New("envx: secret file is blank")

// IsBlankSecretFilePath reports whether the KEY_FILE companion variable for key is
// present in the environment but blank — set to the empty string, or to whitespace
// only — and therefore names no secret file.
//
// SecretWithSource cannot report this state, and deliberately still does not: it gates
// the file channel on a non-empty KEY_FILE, so `KEY_FILE=` is indistinguishable from
// unset and resolves through KEY exactly as if the operator had never written it. That
// is the right rule for a VALUE, which is why every getter here treats empty as unset:
// compose files and CI matrices materialize `KEY=` for a knob left blank. KEY_FILE
// holds a POINTER, though, and a blank pointer is a broken secret mount rather than an
// operator declining to configure one — `KEY_FILE=${SECRETS_DIR}/token` with
// SECRETS_DIR undefined produces exactly this shape.
//
// The state is reported instead of acted on because only the caller knows what its
// secret's absence means, and the two answers are far apart. For a mandatory credential
// the fallthrough surfaces as *MissingError and startup fails anyway; for an OPTIONAL
// one — a bearer token whose absence serves an endpoint unauthenticated — a blank
// KEY_FILE silently disarms the gate, so the fallthrough is fail-OPEN. A caller that
// must refuse that asks before resolving:
//
//	if envx.IsBlankSecretFilePath("BEAT_TOKEN") {
//		return errors.New("BEAT_TOKEN_FILE is set but empty: unset it to configure BEAT_TOKEN directly, or point it at a secret file")
//	}
//	token, src, err := envx.SecretWithSource("BEAT_TOKEN")
//
// Whitespace counts as blank, the rule ErrBlankSecretFile already applies to a secret
// file's CONTENT: a whitespace-only path is a quoting slip, never a filename an operator
// meant to write. Resolution treats the two shapes differently — an empty KEY_FILE is
// ignored, a whitespace-only one is opened and fails — so for that shape this predicate
// buys a diagnostic naming the blank variable in place of a "no such file or directory"
// for a filename nobody can see, not a change of fail direction.
//
// It reports the state and changes none of it: a deployment where `KEY_FILE=` falls
// through to KEY keeps doing so, including for callers that never ask.
func IsBlankSecretFilePath(key Key) bool {
	key.validate()
	path, ok := os.LookupEnv(string(secretFileKey(key)))
	return ok && strings.TrimSpace(path) == ""
}

// secretFileKey names the companion variable holding the path to key's secret file. The
// suffix lives in one place so the resolver and IsBlankSecretFilePath cannot come to
// disagree about which variable they are talking about.
func secretFileKey(key Key) Key {
	return key + "_FILE"
}

// SecretSource reports which channel a secret value came from.
type SecretSource string

// The secret delivery channels SecretWithSource distinguishes.
const (
	// SourceNone means no value was found in either channel.
	SourceNone SecretSource = "none"
	// SourceEnv means the value came from KEY itself.
	SourceEnv SecretSource = "env"
	// SourceFile means the value came from the file named by KEY_FILE.
	SourceFile SecretSource = "file"
)

// SecretWithSource is Secret, additionally reporting which channel supplied the value.
//
// Secret resolves KEY_FILE ahead of KEY and returns only the value, so a caller cannot
// tell whether an environment variable it also set was used or silently ignored. That
// matters when both are set: the operator expressed two intentions and exactly one takes
// effect. Only the caller knows whether that is worth a warning in its own configuration
// vocabulary, so this reports the fact rather than logging it here — consistent with this
// package's rule that the caller owns the policy and envx owns the parse.
//
// A caller that needs the both-channels-set condition composes it in one line:
//
//	v, src, err := envx.SecretWithSource("PFX_PASSWORD")
//	if src == envx.SourceFile && os.Getenv("PFX_PASSWORD") != "" { /* warn */ }
//
// That works on the error paths too, which is where it matters most: an unreadable file
// still reports SourceFile, so the caller can tell the operator that the environment
// variable they also set is not a fallback.
//
// Precedence is unchanged from Secret: KEY_FILE wins when set to a non-empty value,
// because the entire point of the file channel is keeping the value out of the process
// environment. A KEY_FILE that is present but blank names no file and is therefore not
// the file channel at all: resolution falls through to KEY, and IsBlankSecretFilePath is
// how a caller detects that a secret pointer it was given is broken.
func SecretWithSource(key Key) (value string, source SecretSource, err error) {
	key.validate()
	if path := os.Getenv(string(secretFileKey(key))); path != "" {
		data, readErr := readSecretFile(path)
		if readErr != nil {
			return "", SourceFile, fmt.Errorf("read secret file for %s: %w", key, readErr)
		}
		content := string(data)
		// Two different operations on the same bytes, deliberately not folded:
		// blankness is judged on the whitespace-trimmed content (so a file
		// holding only spaces, tabs or newlines is still diagnosed as a broken
		// mount), while the value RETURNED keeps every byte the operator wrote
		// except one trailing line ending.
		if strings.TrimSpace(content) == "" {
			// The path is named because a blank secret file cannot be diagnosed
			// without it; the (absent) VALUE is what must never be logged.
			return "", SourceFile, fmt.Errorf("%w for %s: %s", ErrBlankSecretFile, key, path)
		}
		return trimTrailingLineEnding(content), SourceFile, nil
	}
	v, reqErr := Require(key)
	if reqErr != nil {
		return "", SourceNone, reqErr
	}
	return v, SourceEnv, nil
}

// trimTrailingLineEnding removes at most ONE trailing line ending ("\r\n" or
// "\n") and nothing else.
//
// The file channel used to return strings.TrimSpace of the file's content,
// which made it the only getter in this package that REWRITES a value: the env
// channel hands back os.Getenv verbatim, so a credential with a leading space,
// a trailing tab or an interior NBSP resolved to two different secrets
// depending on which channel delivered it. A consumer that validates a
// credential verbatim — refusing edge whitespace so it never authenticates with
// a value different from the configured one — was silently handed a rewritten
// value through the file channel, with nothing in the error path to reveal it.
//
// A single trailing line ending is still removed because it is not part of the
// value: an editor, a heredoc and `kubectl create secret --from-file` all append
// one, and a file is the one place a value cannot be stored without that
// ambiguity. Everything else — interior whitespace, edge spaces and tabs, a
// SECOND trailing newline, a lone leading newline — is content and is returned
// as-is. A value whose real last byte is a newline cannot be expressed through
// this channel; that is the accepted cost of the convention, and it is why the
// trim is bounded to one line ending rather than all of them. A lone trailing
// "\r" is not a line ending any tool writes here, so it is content too.
func trimTrailingLineEnding(s string) string {
	if !strings.HasSuffix(s, "\n") {
		return s
	}
	return strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r")
}
