package measure

import (
	"strings"
	"testing"
)

// TestHandlerAlreadyServesTheProcessFamilies backs the README's rule: serve
// Registry.Handler alone, because its page already carries every Go and
// process family, and WriteProcess on the same page would repeat each one.
func TestHandlerAlreadyServesTheProcessFamilies(t *testing.T) {
	page := serve(t, NewRegistry("app").Handler())
	for _, f := range processGather() {
		if !strings.Contains(page, "\n# TYPE "+f.Name+" ") {
			t.Errorf("Handler's page has no %s family", f.Name)
		}
	}
}
