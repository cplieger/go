package sse

import (
	"maps"
	"net/http"

	"github.com/cplieger/go/httpserver/v2"
)

// codeSpecs holds exactly the codes this package's writers answer;
// httpservertest.CheckCodeSpecs holds it to the source.
var codeSpecs = map[httpserver.ErrorCode]httpserver.CodeSpec{
	"sse_unavailable":       {Status: http.StatusServiceUnavailable, Remediation: "Reconnect after a short wait; the server is at its stream limit or shutting down.", NoEffect: true},
	"streaming_unsupported": {Status: http.StatusInternalServerError, Remediation: "Report the request id to the server's operator; the server cannot stream events through its response writer."},
	"sse_scope":             {Status: http.StatusBadRequest, Remediation: "Send the topics as a JSON array of valid topic names within the size limit."},
	"scope_conn":            {Status: http.StatusNotFound, Remediation: "Reconnect the stream, then send the scope with the new connection's conn."},
	"scope_forbidden":       {Status: http.StatusForbidden, Remediation: "Change the scope only of a stream opened with the same credential."},
	"scope_stale":           {Status: http.StatusConflict, Remediation: "Send a seq above the last one applied; a later request's scope is in force."},
	"scope_method":          {Status: http.StatusMethodNotAllowed, Remediation: "Resend the request as POST."},
	"scope_content_type":    {Status: http.StatusUnsupportedMediaType, Remediation: "Send the body as application/json."},
	"scope_too_large":       {Status: http.StatusRequestEntityTooLarge, Remediation: "Send fewer topics, so the body fits the size limit."},
	"scope_invalid":         {Status: http.StatusBadRequest, Remediation: "Send a JSON body with conn, a positive seq and topics."},
	"digest_method":         {Status: http.StatusMethodNotAllowed, Remediation: "Resend the request as POST."},
	"digest_content_type":   {Status: http.StatusUnsupportedMediaType, Remediation: "Send the body as application/json."},
	"digest_too_large":      {Status: http.StatusRequestEntityTooLarge, Remediation: "Send fewer subjects per request, so the body fits the size limit."},
	"digest_invalid":        {Status: http.StatusBadRequest, Remediation: "Fix the request body as the message describes, then resend it."},
}

// CodeSpecs returns every code a writer in this package answers (Serve,
// ScopeHandler and DigestHandler), with its status and default remediation,
// for an application's error catalog. The map is the caller's own copy.
func CodeSpecs() map[httpserver.ErrorCode]httpserver.CodeSpec {
	return maps.Clone(codeSpecs)
}

// WithErrors sets the ErrorResponder that writes every refusal the hub
// answers: Serve's, ScopeHandler's and DigestHandler's. Nil means
// httpserver.WriteError, the default, so the last WithErrors wins.
func WithErrors(r httpserver.ErrorResponder) Option {
	return func(c *config) error {
		if r == nil {
			r = httpserver.WriteError
		}
		c.errs = r
		return nil
	}
}
