package httpwiretest_test

import (
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpserver/v2/httpservertest"
)

const codeSpecsApp = "testdata/codespecs/app"

func specsOf(codes ...httpserver.ErrorCode) map[httpserver.ErrorCode]httpserver.CodeSpec {
	m := make(map[httpserver.ErrorCode]httpserver.CodeSpec, len(codes))
	for _, c := range codes {
		m[c] = httpserver.CodeSpec{Status: 409, Remediation: "Fix it."}
	}
	return m
}

// appCodes are the codes the fixture app writes through httpwire's types.
var appCodes = []httpserver.ErrorCode{"name_taken", "note_locked", "note_refused"}

func TestCheckCodeSpecsSeesTheCodesAnAppWritesThroughHTTPWire(t *testing.T) {
	t.Run("the written set passes", func(t *testing.T) {
		r := record(t, func(tb testing.TB) { httpservertest.CheckCodeSpecs(tb, codeSpecsApp, specsOf(appCodes...)) })
		if len(r.failures) != 0 {
			t.Errorf("failures = %q, want none", r.failures)
		}
	})
	t.Run("a written code missing from the specs is named", func(t *testing.T) {
		r := record(t, func(tb testing.TB) {
			httpservertest.CheckCodeSpecs(tb, codeSpecsApp, specsOf("name_taken", "note_locked"))
		})
		if len(r.failures) != 1 || !r.failed(`"note_refused"`) {
			t.Errorf("failures = %q, want one naming note_refused", r.failures)
		}
	})
	t.Run("a code nothing writes is named", func(t *testing.T) {
		r := record(t, func(tb testing.TB) {
			httpservertest.CheckCodeSpecs(tb, codeSpecsApp, specsOf(append(appCodes, "never_written")...))
		})
		if len(r.failures) != 1 || !r.failed(`"never_written"`) {
			t.Errorf("failures = %q, want one naming never_written", r.failures)
		}
	})
}
