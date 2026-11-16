package httpservertest

import (
	"net/http"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
)

// CheckSPA is not written yet.
func CheckSPA(t testing.TB, h http.Handler, c httpserver.SPAConfig) { //nolint:gocritic // hugeParam: SPA's own value config
	t.Helper()
	_, _ = h, c
}

// CheckNoUI is not written yet.
func CheckNoUI(t testing.TB, h http.Handler) {
	t.Helper()
	_ = h
}
