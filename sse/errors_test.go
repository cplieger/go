package sse

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpserver/v2/httpservertest"
)

func TestCodeSpecs_matchesSource(t *testing.T) {
	httpservertest.CheckCodeSpecs(t, ".", CodeSpecs())
}

func TestCodeSpecs_hubRefusals(t *testing.T) {
	specs := CodeSpecs()
	tests := []struct {
		code       httpserver.ErrorCode
		wantStatus int
		wantNoEff  bool
	}{
		{code: "sse_unavailable", wantStatus: http.StatusServiceUnavailable, wantNoEff: true},
		{code: "streaming_unsupported", wantStatus: http.StatusInternalServerError, wantNoEff: false},
	}
	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			spec, ok := specs[tt.code]
			if !ok {
				t.Fatalf("CodeSpecs() has no %q", tt.code)
			}
			if spec.Status != tt.wantStatus || spec.NoEffect != tt.wantNoEff {
				t.Errorf("CodeSpecs()[%q] = status %d NoEffect %v, want %d %v", tt.code, spec.Status, spec.NoEffect, tt.wantStatus, tt.wantNoEff)
			}
			if spec.Remediation == "" {
				t.Errorf("CodeSpecs()[%q] has no remediation", tt.code)
			}
		})
	}
}

func TestCodeSpecs_returnsACopy(t *testing.T) {
	CodeSpecs()["sse_unavailable"] = httpserver.CodeSpec{}
	if CodeSpecs()["sse_unavailable"].Status != http.StatusServiceUnavailable {
		t.Error("a write to CodeSpecs()'s map reached the next call")
	}
}

// refusal is one call a recordingResponder saw.
type refusal struct {
	r      *http.Request
	code   httpserver.ErrorCode
	status int
}

type recordingResponder struct{ calls []refusal }

func (rr *recordingResponder) respond(w http.ResponseWriter, r *http.Request, status int, code httpserver.ErrorCode, _ string) {
	rr.calls = append(rr.calls, refusal{r: r, code: code, status: status})
	w.WriteHeader(status)
}

// hubRefusal makes h refuse one request with code, for each refusal a hub
// writes before a stream upgrades.
type hubRefusal struct {
	serve func(t *testing.T, h *Hub, r *http.Request) *httptest.ResponseRecorder
	code  httpserver.ErrorCode
}

var quiet = WithLogger(slog.New(slog.DiscardHandler))

var hubRefusals = []hubRefusal{
	{code: "streaming_unsupported", serve: func(_ *testing.T, h *Hub, r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.Serve(noFlush{rec}, r)
		return rec
	}},
	{code: "sse_unavailable", serve: func(t *testing.T, h *Hub, r *http.Request) *httptest.ResponseRecorder {
		if err := h.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.Serve(rec, r)
		return rec
	}},
}

// noFlush hides the recorder's Flush, so Serve finds no http.Flusher.
type noFlush struct{ rec *httptest.ResponseRecorder }

func (n noFlush) Header() http.Header         { return n.rec.Header() }
func (n noFlush) Write(b []byte) (int, error) { return n.rec.Write(b) }
func (n noFlush) WriteHeader(code int)        { n.rec.WriteHeader(code) }

func TestWithErrors_hubRefusalsGoThroughTheResponder(t *testing.T) {
	for _, tt := range hubRefusals {
		t.Run(string(tt.code), func(t *testing.T) {
			var rr recordingResponder
			h := mustNew(t, quiet, WithErrors(rr.respond))
			r := httptest.NewRequest(http.MethodGet, "/events", http.NoBody)
			rec := tt.serve(t, h, r)
			want := []refusal{{r: r, code: tt.code, status: CodeSpecs()[tt.code].Status}}
			if !slices.Equal(rr.calls, want) {
				t.Fatalf("responder calls = %+v, want %+v", rr.calls, want)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("body = %q, want only what the responder wrote", rec.Body)
			}
		})
	}
}

func TestWithErrors_absentOrNilAnswersWriteError(t *testing.T) {
	options := map[string][]Option{"absent": {quiet}, "nil": {quiet, WithErrors(nil)}}
	for _, tt := range hubRefusals {
		for name, opts := range options {
			t.Run(string(tt.code)+"_"+name, func(t *testing.T) {
				ctx := httpserver.WithRequestID(t.Context(), "req-1")
				r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/events", http.NoBody)
				got := tt.serve(t, mustNew(t, opts...), r)

				want := httptest.NewRecorder()
				httpserver.WriteError(want, r, got.Code, tt.code, wantMessage[tt.code])
				if got.Code != CodeSpecs()[tt.code].Status {
					t.Errorf("status = %d, want %d", got.Code, CodeSpecs()[tt.code].Status)
				}
				if got.Body.String() != want.Body.String() {
					t.Errorf("body = %s, want %s", got.Body, want.Body)
				}
				if got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
					t.Errorf("Content-Type = %q, want %q", got.Header().Get("Content-Type"), want.Header().Get("Content-Type"))
				}
			})
		}
	}
}

var wantMessage = map[httpserver.ErrorCode]string{
	"streaming_unsupported": "streaming not supported",
	"sse_unavailable":       "sse unavailable",
}

func TestWithErrors_handlerRefusalsGoThroughTheResponder(t *testing.T) {
	var rr recordingResponder
	h := mustNew(t, quiet, withEpoch(testEpoch), WithErrors(rr.respond))
	scope, err := h.ScopeHandler(func(*http.Request, any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	digest := mustDigestHandler(t, h, func(context.Context, []Held) ([]State, error) { return nil, nil })

	connect := httptest.NewRequest(http.MethodGet, "/events", http.NoBody)
	connect.Header.Set(scopeHeader, `[""]`)
	h.Serve(httptest.NewRecorder(), connect, WithRequestScope("p"))
	scopeGet := httptest.NewRequest(http.MethodGet, "/scope", http.NoBody)
	scope.ServeHTTP(httptest.NewRecorder(), scopeGet)
	digestGet := httptest.NewRequest(http.MethodGet, "/digest", http.NoBody)
	digest.ServeHTTP(httptest.NewRecorder(), digestGet)

	want := []refusal{
		{r: connect, code: "sse_scope", status: http.StatusBadRequest},
		{r: scopeGet, code: "scope_method", status: http.StatusMethodNotAllowed},
		{r: digestGet, code: "digest_method", status: http.StatusMethodNotAllowed},
	}
	if !slices.Equal(rr.calls, want) {
		t.Errorf("responder calls = %+v, want %+v", rr.calls, want)
	}
}

// Only the option and its default may name httpserver's envelope writers, so
// every refusal a writer adds goes through the hub's responder.
func TestWithErrors_noWriterBypassesTheResponder(t *testing.T) {
	writers := map[string]bool{"WriteError": true, "NewErrorResponse": true, "ErrorResponse": true}
	allowed := map[string]bool{"errors.go": true, "config.go": true}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || allowed[name] {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		qualifier := ""
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == "github.com/cplieger/go/httpserver/v2" {
				qualifier = "httpserver"
				if imp.Name != nil {
					qualifier = imp.Name.Name
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && qualifier != "" && x.Name == qualifier && writers[sel.Sel.Name] {
				t.Errorf("%s: httpserver.%s bypasses the hub's responder; write through h.cfg.errs", fset.Position(sel.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
}
