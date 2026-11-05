package yamlenv

import (
	"bytes"
	"errors"
	"io"

	"go.yaml.in/yaml/v4"
)

// ErrMultipleDocuments is returned by CheckSingleDocument when the input
// carries content beyond its first YAML document. It is fully static — it
// embeds nothing from the input — so it is safe to log without
// SanitizeDecodeError.
var ErrMultipleDocuments = errors.New("yamlenv: more than one YAML document; remove the '---' separator")

// CheckUnknownKeys loads data into probe, a pointer to a fresh throwaway value
// of the caller's config type, with go-yaml's WithKnownFields, so a misspelled
// or misplaced key fails instead of being ignored. Run it on the RAW
// pre-expansion bytes: expansion cannot change which keys exist, positions
// then match the file, and an alias used as a key names the literal ${VAR}. An
// empty document passes. The error is a *yaml.LoadErrors, or a *yaml.LoadError
// for a parse error or an aborted load; either may embed document content, so
// pass it through SanitizeDecodeError before logging it.
func CheckUnknownKeys(data []byte, probe any) error {
	l, err := yaml.NewLoader(bytes.NewReader(data), yaml.WithKnownFields())
	if err != nil {
		return err
	}
	if err := l.Load(probe); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// CheckSingleDocument rejects input that carries more than one YAML
// document, so nothing below a stray "---" separator is silently ignored. Run
// it on the raw pre-expansion bytes; Load runs the same check in its parse. A
// first document that fails to parse returns nil: the caller's parse owns that
// diagnostic. The only non-nil return is ErrMultipleDocuments, which embeds no
// input content and is safe to log unsanitized.
func CheckSingleDocument(data []byte) error {
	if _, err := parseSingleDocument(data); errors.Is(err, ErrMultipleDocuments) {
		return err
	}
	return nil
}

// parseSingleDocument parses data into its only document node. It returns a
// nil node for an empty stream, ErrMultipleDocuments when anything follows
// the first document (even the empty one a trailing separator produces, or a
// syntax error), and go-yaml's error when the first document fails to parse.
func parseSingleDocument(data []byte) (*yaml.Node, error) {
	l, err := yaml.NewLoader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := l.Load(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	var next yaml.Node
	if err := l.Load(&next); !errors.Is(err, io.EOF) {
		return nil, ErrMultipleDocuments
	}
	return &doc, nil
}
