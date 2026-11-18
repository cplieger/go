package httpwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/cplieger/go/httpserver/v2"
)

// writeAnswer writes a JSON row's success; code zero is the row's primary
// status. The body is encoded before the status is sent, so a body that
// cannot encode still answers internal_error.
func (a *API) writeAnswer(w http.ResponseWriter, r *http.Request, ep *Endpoint, code int, body any) {
	if code == 0 {
		code = primaryStatus(ep)
	}
	declared, hasBody := declaredStatus(ep, code)
	if !declared {
		a.log().ErrorContext(r.Context(), "httpwire: a handler answered a status its row does not declare", "row", ep.Name, "status", code)
		a.table.Catalog.WriteError(w, r, &Error{Code: codeInternal})
		return
	}
	if !hasBody {
		w.WriteHeader(code)
		return
	}
	if ep.List {
		var problem string
		if body, problem = checkedPage(body); problem != "" {
			a.log().ErrorContext(r.Context(), "httpwire: a List row answered an inconsistent page", "row", ep.Name, "problem", problem)
			a.table.Catalog.WriteError(w, r, &Error{Code: codeInternal})
			return
		}
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		a.log().ErrorContext(r.Context(), "httpwire: a handler's answer does not encode as JSON", "row", ep.Name, "error", err)
		a.table.Catalog.WriteError(w, r, &Error{Code: codeInternal})
		return
	}
	httpserver.JSONHeaders(w)
	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}

// declaredStatus reports whether ep declares code and whether that status
// carries a body.
func declaredStatus(ep *Endpoint, code int) (declared, body bool) {
	if code == primaryStatus(ep) {
		return true, ep.Response != nil
	}
	for _, s := range ep.Successes {
		if s.Status == code {
			return true, s.Response != nil
		}
	}
	return false, false
}

func (a *API) writeError(w http.ResponseWriter, r *http.Request, ep *Endpoint, err error) {
	a.table.Catalog.WriteError(w, r, a.mapError(r, ep, err))
}

// mapError answers a handler's error: its *Error when the row's union holds
// the code, else the first owner sentinel it wraps, with the sentinel's own
// text, since the wrapped text may name paths. Anything else is a bug,
// answered internal_error.
func (a *API) mapError(r *http.Request, ep *Endpoint, err error) *Error {
	ctx := r.Context()
	e, isErr := errors.AsType[*Error](err)
	if isErr && e != nil && slices.Contains(ep.Errors, e.Code) {
		return e
	}
	for _, o := range ep.Owners {
		for _, oc := range o.Codes {
			if errors.Is(err, oc.Err) {
				if spec, _ := a.table.Catalog.Lookup(oc.Code); spec.Status >= http.StatusInternalServerError {
					a.log().WarnContext(ctx, "httpwire: a handler answered a server-side owner code", "row", ep.Name, "code", oc.Code)
				}
				return &Error{Code: oc.Code, Message: oc.Err.Error()}
			}
		}
	}
	level := slog.LevelError
	if ctx.Err() != nil {
		level = slog.LevelDebug
	}
	switch {
	case isErr && e == nil:
		a.log().Log(ctx, level, "httpwire: a handler returned a nil *Error as its error", "row", ep.Name)
	case isErr:
		a.log().Log(ctx, level, "httpwire: a handler answered a code outside its row's union", "row", ep.Name, "code", e.Code)
	default:
		a.log().Log(ctx, level, "httpwire: a handler's error matches no code of its row", "row", ep.Name, "error", err)
	}
	return &Error{Code: codeInternal}
}
