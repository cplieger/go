package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
)

const (
	cspSelf = "'self'"
	cspNone = "'none'"
)

// spaDirectives is SPA's default Content-Security-Policy, in the order it is
// written.
var spaDirectives = []struct {
	name    string
	sources []string
}{
	{"default-src", []string{cspSelf}},
	{"script-src", []string{cspSelf}},
	{"style-src", []string{cspSelf}},
	{"img-src", []string{cspSelf, "data:"}},
	{"font-src", []string{cspSelf}},
	{"connect-src", []string{cspSelf}},
	{"worker-src", []string{cspSelf}},
	{"object-src", []string{cspNone}},
	{"base-uri", []string{cspNone}},
	{"frame-ancestors", []string{cspNone}},
	{"form-action", []string{cspSelf}},
}

// cspAddable is every directive SPAConfig.CSPAdd may extend and every source
// it may add. media-src is absent from the default and starts from 'self'.
var (
	cspAddable        = []string{"img-src", "media-src", "font-src"}
	cspAddableSources = []string{"blob:", "data:"}
)

// SPAPolicy returns the Content-Security-Policy SPA writes for add, the
// SPAConfig.CSPAdd value, and the error SPA refuses add with: a directive
// other than img-src, media-src or font-src, a source other than blob: or
// data:, an empty list, and a source the directive already holds.
func SPAPolicy(add map[string][]string) (string, error) {
	for _, d := range slices.Sorted(maps.Keys(add)) {
		if !slices.Contains(cspAddable, d) {
			return "", fmt.Errorf("httpserver: CSPAdd extends %q; only %s take additions", d, strings.Join(cspAddable, ", "))
		}
		if len(add[d]) == 0 {
			return "", fmt.Errorf("httpserver: CSPAdd gives %s no source", d)
		}
	}
	directives := make([]string, 0, len(spaDirectives)+1)
	for _, d := range spaDirectives {
		v, err := cspDirective(d.name, d.sources, add[d.name])
		if err != nil {
			return "", err
		}
		directives = append(directives, v)
	}
	if extra, ok := add["media-src"]; ok {
		v, err := cspDirective("media-src", []string{cspSelf}, extra)
		if err != nil {
			return "", err
		}
		directives = append(directives, v)
	}
	return strings.Join(directives, "; "), nil
}

// cspDirective writes name with base and the sorted extra sources.
func cspDirective(name string, base, extra []string) (string, error) {
	sources := slices.Clone(base)
	for _, src := range slices.Sorted(slices.Values(extra)) {
		if !slices.Contains(cspAddableSources, src) {
			return "", fmt.Errorf("httpserver: CSPAdd adds %q to %s; only %s may be added", src, name, strings.Join(cspAddableSources, " and "))
		}
		if slices.Contains(sources, src) {
			return "", fmt.Errorf("httpserver: CSPAdd adds %q to %s, which already holds it", src, name)
		}
		sources = append(sources, src)
	}
	return name + " " + strings.Join(sources, " "), nil
}

// policyETag is the entity-tag of a no-cache answer: it binds the file's
// manifest hash and the policy written with it, so a changed policy changes
// the tag and a revalidation answers 200 with the new header rather than a
// 304 whose headers the browser may not merge (RFC 9111 section 4.3.4).
func policyETag(fileHash, policy string) string {
	sum := sha256.Sum256([]byte(fileHash + "\x00" + policy))
	return `"` + hex.EncodeToString(sum[:])[:32] + `"`
}
