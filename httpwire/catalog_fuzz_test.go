package httpwire_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

// FuzzWriteErrorDecodes holds the writer's promise: whatever a handler puts
// in an Error, the body decodes and the status is the written code's.
func FuzzWriteErrorDecodes(f *testing.F) {
	f.Add("stale_version", "Saved elsewhere.", "row_changed", "items[0]", "range", "sonarr", 401, "Unauthorized", int64(1500))
	f.Add("no_such_code", "\xff\xfe", "Bad Reason", "", "", "Sonarr", 42, "\x1b[31m\nlong", int64(-1))
	f.Add("internal_error", strings.Repeat("é", 3000), strings.Repeat("a", 64), "p", "c", "github", 599, strings.Repeat("\u2028", 400), int64(0))
	c := httpwire.NewCatalog()
	f.Fuzz(func(t *testing.T, code, msg, reason, path, fcode, service string, status int, upstream string, retryMS int64) {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
		rec := httptest.NewRecorder()
		c.WriteError(rec, r, &httpwire.Error{
			Code: httpserver.ErrorCode(code), Message: msg, Reason: reason,
			Fields:     []httpwire.FieldError{{Path: path, Code: fcode, Message: msg}},
			RetryAfter: time.Duration(retryMS) * time.Millisecond,
			Upstream:   &httpwire.Upstream{Service: service, Status: status, Message: upstream},
		})
		env, err := httpwire.DecodeEnvelope(rec.Body.Bytes())
		if err != nil {
			t.Fatalf("DecodeEnvelope(%s) = %v", rec.Body.Bytes(), err)
		}
		spec, ok := c.Lookup(env.Code)
		if !ok || rec.Code != spec.Status {
			t.Errorf("status %d for code %q; want the catalog's %d", rec.Code, env.Code, spec.Status)
		}
		if env.Upstream != nil && len(env.Upstream.Message) > 512 {
			t.Errorf("upstream.message is %d bytes", len(env.Upstream.Message))
		}
	})
}
