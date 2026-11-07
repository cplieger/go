package httpclient

// NewStatusErrorForURL builds the *StatusError GetBytes returns for rawURL, so
// the external tests can pin how Error() renders a URL.
func NewStatusErrorForURL(rawURL string, code int) *StatusError {
	return &StatusError{rawURL: rawURL, Code: code}
}

// StatusErrorURL returns the unredacted URL e carries.
func StatusErrorURL(e *StatusError) string { return e.rawURL }
