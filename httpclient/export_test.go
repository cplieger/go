package httpclient

import "net/http"

// NewStatusErrorForURL builds the *StatusError GetBytes returns for rawURL, so
// the external tests can pin how Error() renders a URL.
func NewStatusErrorForURL(rawURL string, code int) *StatusError {
	return newStatusError(&http.Response{StatusCode: code, Header: http.Header{}}, rawURL)
}

// StatusErrorURL returns the URL e carries, redacted when e was built.
func StatusErrorURL(e *StatusError) string { return e.redacted }
