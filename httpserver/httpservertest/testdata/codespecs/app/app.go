package app

import (
	"net/http"

	hs "github.com/cplieger/go/httpserver/v2"
)

const (
	constCode hs.ErrorCode = "const_code"
	notACode               = "not a code"
)

type refuser func(w http.ResponseWriter, code hs.ErrorCode, msg string)

type config struct {
	respond hs.ErrorResponder
	deny    refuser
	code    hs.ErrorCode
	msg     string
}

func newConfig() *config {
	return &config{code: "field_code", msg: notACode}
}

func withCode(code hs.ErrorCode) func(*config) {
	return func(c *config) { c.code = code }
}

var _ = withCode("option_code")

func (c *config) serve(w http.ResponseWriter, r *http.Request) {
	hs.WriteError(w, r, http.StatusNotFound, "literal_code", notACode)
	hs.WriteError(w, r, http.StatusConflict, constCode, "m")
	hs.WriteError(w, r, http.StatusConflict, hs.ErrorCode("converted_code"), "m")
	hs.WriteError(w, r, http.StatusConflict, c.code, "not followed")
	c.respond(w, r, http.StatusGone, "responder_code", "m")
	c.deny(w, "func_type_code", "m")
	http.Error(w, "plain_error_text", http.StatusTeapot)
}
