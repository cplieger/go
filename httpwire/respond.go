package httpwire

import (
	"errors"
	"io"
	"maps"
	"net/http"
	"strconv"
)

const (
	headerWireVersion  = "Wire-Version"
	headerFeedPosition = "Feed-Position"
)

// answerWriter carries a row's answer: it adds Feed-Position to a 2xx,
// logs a success status a Raw, Stream or Redirect handler writes that its
// row does not declare, and answers body_too_large in place of a handler's
// answer once the handler read past the row's body cap.
type answerWriter struct {
	http.ResponseWriter
	a        *API
	r        *http.Request
	ep       *Endpoint
	body     *cappedBody
	pre      http.Header // the header before the handler ran
	position string
	own      bool // the handler writes its own status
	wrote    bool
	dropped  bool // the handler's answer is replaced, so its writes go nowhere
}

func (w *answerWriter) WriteHeader(code int) {
	switch {
	case w.wrote:
		if !w.dropped {
			w.ResponseWriter.WriteHeader(code)
		}
		return
	case code < http.StatusOK:
		w.ResponseWriter.WriteHeader(code)
		return
	case w.body != nil && w.body.over:
		w.refuseBody()
		return
	}
	w.wrote = true
	if w.position != "" && code/100 == 2 {
		w.Header().Set(headerFeedPosition, w.position)
	}
	if w.own && w.undeclared(code) {
		w.a.log().ErrorContext(w.r.Context(), "httpwire: a handler wrote a success status its row does not declare", "row", w.ep.Name, "status", code)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *answerWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.dropped {
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}

// FlushError and Flush keep a stream flushable through the wrap, for a
// handler asserting http.Flusher and for http.ResponseController alike.
func (w *answerWriter) FlushError() error {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.dropped {
		return nil
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *answerWriter) Flush() { _ = w.FlushError() }

func (w *answerWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// finish answers for a handler that returned having written nothing: the
// cap's refusal, else the 200 net/http would send, through the status check.
func (w *answerWriter) finish() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
}

// undeclared reports a success status (a 2xx, and a 3xx on a Redirect
// row) the row does not declare.
func (w *answerWriter) undeclared(code int) bool {
	success := code/100 == 2 || w.ep.Kind == Redirect && code/100 == 3
	declared, _ := declaredStatus(w.ep, code)
	return success && !declared
}

// refuseBody replaces the handler's answer with the cap's refusal.
func (w *answerWriter) refuseBody() {
	w.restoreHeader()
	w.wrote, w.dropped = true, true
	w.a.table.Catalog.WriteError(w.ResponseWriter, w.r, tooLarge(w.body.limit))
}

// restoreHeader puts back the header the row started with, the binder's
// and the app's middleware's, before a refusal replaces a handler's
// answer: what the handler set or overwrote is undone.
func (w *answerWriter) restoreHeader() {
	h := w.Header()
	clear(h)
	maps.Copy(h, w.pre)
}

// cappedBody notes when its reader hits the cap http.MaxBytesReader holds
// it to.
type cappedBody struct {
	io.ReadCloser
	limit int
	over  bool
}

func (b *cappedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		b.over = true
	}
	return n, err
}

func tooLarge(limit int) *Error {
	return &Error{Code: "body_too_large", Message: "the body is over " + strconv.Itoa(limit) + " bytes"}
}
