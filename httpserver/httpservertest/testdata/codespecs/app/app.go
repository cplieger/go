package app

import (
	"net/http"

	hs "github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpserver/v2/httpservertest/testdata/codespecs/codes"
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
	hs.WriteErrorBody(w, http.StatusConflict, hs.NewErrorResponse(r, "constructor_code", "m"))
	hs.WriteErrorBody(w, http.StatusConflict, hs.ErrorResponse{Code: "envelope_code"})
	http.Error(w, "plain_error_text", http.StatusTeapot)
	hs.WriteError(w, r, http.StatusConflict, codes.Shared, "imported constant")
	hs.WriteError(w, r, http.StatusConflict, codes.Concatenated, "concatenation")
	hs.WriteError(w, r, http.StatusConflict, (hs.ErrorCode)("paren_"+"code"), "m")
	codes.Register("registered_code")
	codes.Responder{}.Respond(w, r, http.StatusGone, "other_value_code", "m")
	_ = codes.Spec{Code: "other_struct_code"}
	_ = map[hs.ErrorCode]int{"map_key_code": 1}
	_ = pair{"positional_code", 1}
	c.warn(w, "variadic_code", "second_variadic_code")
}

type pair struct {
	code hs.ErrorCode
	n    int
}

func (*config) warn(_ http.ResponseWriter, codes ...hs.ErrorCode) {}
