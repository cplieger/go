package httpwire

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
)

// FieldCodes is the field-code map's codes in the order the TS half's
// SCHEMA_FIELD_CODES lists them.
var FieldCodes = []string{codeMalformed, codeRequired, codeUnknown, codeEnum, codeUnion, codeRange, codePattern}

// CatalogCodes answers every code c holds, sorted.
func CatalogCodes(c *Catalog) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Sorted(maps.Keys(c.specs))
}

// ParseIfMatch reads v as an IfMatch row's If-Match header.
func ParseIfMatch(v string) (Tag, bool) {
	r := httptest.NewRequest(http.MethodPut, "/", nil)
	r.Header.Set(headerIfMatch, v)
	t, e := ifMatch(r)
	return t, e == nil
}

// ParseIdempotencyKey reads v as a Keyed row's Idempotency-Key header.
func ParseIdempotencyKey(v string) (string, bool) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set(headerIdempotencyKey, v)
	key, e := idempotencyKeyOf(r, &Endpoint{Keyed: true})
	return key, e == nil
}
