package httpcache

import (
	"net/http"
	"strings"
	"testing"
)

// FuzzCacheValidator_wellFormed holds the validators the cache replays from an
// untrusted server to RFC 9110: a replayed ETag is the server's one entity-tag
// unchanged (section 8.8.3), a replayed Last-Modified its one HTTP date unchanged
// (section 8.8.2), each at most 1 KiB and free of bytes a field value may not
// carry, so a replayed validator never fails a request at write time.
func FuzzCacheValidator_wellFormed(f *testing.F) {
	for _, seed := range []string{
		`"v1"`, `W/"v1"`, `""`, `*`, `"v1", "v2"`, `v1`, `"v1"x`, `w/"v1"`, "\"et\r\nX: 1\"", "\"et\x7fag\"",
		"Wed, 25 Oct 2023 19:17:59 GMT", "Wed, 25 Oct 2023 19:17:59 GMT\r\nX: 1", "yesterday",
		`"` + strings.Repeat("v", maxValidatorBytes-2) + `"`, `"` + strings.Repeat("v", maxValidatorBytes-1) + `"`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, v string) {
		h := http.Header{"Etag": {v}, "Last-Modified": {v}}
		for name, got := range map[string]string{"ETag": entityTag(h), "Last-Modified": httpDate(h)} {
			if got == "" {
				continue
			}
			if got != v {
				t.Errorf("the %s replayed from %q = %q, want the server's value unchanged or none", name, v, got)
			}
			if len(got) > maxValidatorBytes || !validFieldValue(got) {
				t.Errorf("the %s replayed from %q is %d bytes or carries a byte a field value may not, want neither", name, v, len(got))
			}
		}
		if tag := entityTag(h); tag != "" {
			opaque := strings.TrimPrefix(tag, "W/")
			if len(opaque) < 2 || opaque[0] != '"' || opaque[len(opaque)-1] != '"' || strings.Count(opaque, `"`) != 2 || strings.ContainsAny(opaque, " \t") {
				t.Errorf("the ETag replayed from %q is not one entity-tag", v)
			}
		}
		if date := httpDate(h); date != "" {
			if _, err := http.ParseTime(date); err != nil {
				t.Errorf("the Last-Modified replayed from %q is not an HTTP date: %v", v, err)
			}
		}
	})
}
