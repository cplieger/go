package httpserver

import "net/http"

// CrossOrigin serves next behind http.CrossOriginProtection, answering a
// refused request with cross_origin_refused (403) as the envelope through errs
// (nil: WriteError). It refuses an unsafe method whose Sec-Fetch-Site is not
// same-origin or none, or, with no Sec-Fetch-Site, whose Origin is not the
// request's host; GET, HEAD, OPTIONS and a request with neither header pass.
func CrossOrigin(next http.Handler, errs ErrorResponder) http.Handler {
	if errs == nil {
		errs = WriteError
	}
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		errs(w, r, http.StatusForbidden, "cross_origin_refused", "cross-origin request refused")
	}))
	return cop.Handler(next)
}
