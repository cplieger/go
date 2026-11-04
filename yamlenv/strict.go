package yamlenv

import (
	"bytes"
	"errors"
	"io"

	"go.yaml.in/yaml/v3"
)

// ErrMultipleDocuments is returned by CheckSingleDocument when the input
// carries content beyond its first YAML document. It is fully static — it
// embeds nothing from the input — so it is safe to log without
// SanitizeDecodeError.
var ErrMultipleDocuments = errors.New("yamlenv: more than one YAML document; remove the '---' separator")

// CheckUnknownKeys fails loudly on a key the config type does not declare:
// it re-decodes data with yaml.v3's KnownFields(true) into probe, so a
// misspelled or misplaced key errors ("line N: field X not found in type T")
// instead of being silently ignored while its intended setting stays at the
// default. probe must be a pointer to a fresh throwaway value of the
// caller's config struct type; the decode mutates it and nothing reads it
// afterwards.
//
// Run it on the RAW pre-expansion bytes, beside Expand rather than after it:
// expansion rewrites string scalar values only (keys stay literal), so it
// cannot change which keys exist, and the error's line numbers then point at
// the file the operator actually wrote. An empty document passes — absence
// of keys is not an unknown key, and emptiness policy belongs to the
// caller's own parse and decode steps.
//
// The returned error is yaml.v3's and may embed document content (the key
// name; an accompanying wrong-type entry can embed a scalar excerpt), so a
// caller that logs it at startup should pass it through SanitizeDecodeError,
// which recognizes the unknown-key entry shape and rewrites it
// value-independently.
func CheckUnknownKeys(data []byte, probe any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(probe); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// CheckSingleDocument rejects input that carries more than one YAML
// document. Single-document parse pipelines (yaml.Unmarshal, one
// Decoder.Decode, CheckUnknownKeys above) consume only the first document,
// so everything below a stray "---" separator would otherwise be silently
// ignored — the opposite of a fail-loud config loader. Like CheckUnknownKeys
// it runs on the raw pre-expansion bytes; expansion is post-parse and
// string-values-only, so it cannot change how many documents exist.
//
// A first document that fails to parse returns nil deliberately: the
// caller's parse and decode steps own those diagnostics, this check owns
// only document multiplicity. The only non-nil return is
// ErrMultipleDocuments, which embeds no input content and is safe to log
// unsanitized.
func CheckSingleDocument(data []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil
	}
	if err := dec.Decode(&doc); !errors.Is(err, io.EOF) {
		// Anything but EOF — a second document (even the empty one a
		// trailing separator produces) or a syntax error inside it — means
		// content beyond the first document.
		return ErrMultipleDocuments
	}
	return nil
}
