package httpwire_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/httpwire"
)

type (
	noteID     string
	noteParams struct {
		ID noteID `json:"id"`
	}
	noteBody struct {
		Text label `json:"text"`
	}
	mergeBody struct {
		Base httpwire.Tag `json:"base"`
	}
	note struct {
		Text string       `json:"text"`
		Tag  httpwire.Tag `json:"tag"`
	}
)

func (noteID) MaxBytes() int { return 16 }

func ifMatchDoc(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/openapi/ifmatch.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ifMatchTable is a read answering a tag, the write that takes it back in
// If-Match, and a write carrying a tag in its body.
func ifMatchTable() *httpwire.Table {
	return &httpwire.Table{
		Prefix: "/api", Catalog: httpwire.NewCatalog(),
		Endpoints: []httpwire.Endpoint{
			{
				Name: "notes.read", Method: http.MethodGet, Path: "/api/notes/{id}", Summary: "Read a note",
				Description: "Reads one note and its tag.", Params: reflect.TypeFor[noteParams](), Response: reflect.TypeFor[note](),
				Access: public,
			},
			{
				Name: "notes.save", Method: http.MethodPut, Path: "/api/notes/{id}", Summary: "Save a note",
				Description: "Saves a note read at its tag.", Params: reflect.TypeFor[noteParams](), Request: reflect.TypeFor[noteBody](),
				Access: public, IfMatch: true,
			},
			{
				Name: "notes.merge", Method: http.MethodPost, Path: "/api/notes/{id}/merge", Summary: "Merge a note",
				Description: "Merges a note from a base tag.", Params: reflect.TypeFor[noteParams](), Request: reflect.TypeFor[mergeBody](),
				Access: public,
			},
		},
	}
}

var (
	thisOpen  = httpwire.OpenToken([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	otherOpen = httpwire.OpenToken([16]byte{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1})
)

// noteStore is one note whose handlers compare tags whole, as an IfMatch
// row's handler does inside its write.
type noteStore struct {
	api     *httpwire.API
	got     []httpwire.Tag
	saved   []string
	current httpwire.Tag
}

func newNoteStore(t *testing.T, doc []byte) *noteStore {
	t.Helper()
	s := &noteStore{current: httpwire.Tag{Open: thisOpen, Rev: 7}}
	api, err := httpwire.New(ifMatchTable(), []httpwire.Binding{
		httpwire.Bind("notes.read", func(context.Context, httpwire.Call[noteParams, noQ, noB, noH]) (note, error) {
			return note{Text: "hello", Tag: s.current}, nil
		}),
		httpwire.Bind("notes.save", func(_ context.Context, c httpwire.Call[noteParams, noQ, noteBody, noH]) (httpwire.None, error) {
			s.got = append(s.got, c.IfMatch)
			if c.IfMatch != s.current {
				return httpwire.None{}, &httpwire.Error{Code: "stale_version", Message: "the note changed since it was read"}
			}
			s.saved = append(s.saved, string(c.Body.Text))
			s.current.Rev++
			return httpwire.None{}, nil
		}),
		httpwire.Bind("notes.merge", func(_ context.Context, c httpwire.Call[noteParams, noQ, mergeBody, noH]) (httpwire.None, error) {
			s.got = append(s.got, c.IfMatch, c.Body.Base)
			return httpwire.None{}, nil
		}),
	}, httpwire.WithSchema(doc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.api = api
	return s
}

// serveIfMatch answers one request carrying each value as its own If-Match
// line.
func serveIfMatch(api http.Handler, method, target, body string, ifMatch ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for _, v := range ifMatch {
		r.Header.Add("If-Match", v)
	}
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, r)
	return rec
}

func quotedTag(t httpwire.Tag) string { return `"` + t.String() + `"` }

func TestIfMatchOtherOpenIsStale(t *testing.T) {
	s := newNoteStore(t, ifMatchDoc(t))
	read := serve(s.api, http.MethodGet, "/api/notes/a", "")
	var n note
	if err := json.Unmarshal(read.Body.Bytes(), &n); read.Code != http.StatusOK || err != nil {
		t.Fatalf("read answered %d %q (%v)", read.Code, read.Body.String(), err)
	}
	if !strings.Contains(read.Body.String(), `"tag":"`+n.Tag.String()+`"`) {
		t.Errorf("read body %q does not carry the tag as its string", read.Body.String())
	}
	stale := httpwire.Tag{Open: otherOpen, Rev: n.Tag.Rev}
	rec := serveIfMatch(s.api, http.MethodPut, "/api/notes/a", `{"text":"x"}`, quotedTag(stale))
	if env := envelopeOf(t, rec); rec.Code != http.StatusPreconditionFailed || env.Code != "stale_version" {
		t.Errorf("another open at the same revision answered %d %q, want 412 stale_version", rec.Code, env.Code)
	}
	if len(s.saved) != 0 || len(s.got) != 1 || s.got[0] != stale {
		t.Errorf("handler saw %v and saved %v, want the stale tag and no save", s.got, s.saved)
	}
	rec = serveIfMatch(s.api, http.MethodPut, "/api/notes/a", `{"text":"y"}`, quotedTag(n.Tag))
	if rec.Code != http.StatusNoContent || !slices.Equal(s.saved, []string{"y"}) {
		t.Errorf("the read's own tag answered %d %q, saved %v; want 204 and one save", rec.Code, rec.Body.String(), s.saved)
	}
	rec = serveIfMatch(s.api, http.MethodPut, "/api/notes/a", `{"text":"z"}`, quotedTag(n.Tag))
	if env := envelopeOf(t, rec); rec.Code != http.StatusPreconditionFailed || env.Code != "stale_version" {
		t.Errorf("the read's tag after a save answered %d %q, want 412 stale_version", rec.Code, env.Code)
	}
}

func TestIfMatchBareIntegerInvalid(t *testing.T) {
	good := quotedTag(httpwire.Tag{Open: thisOpen, Rev: 7})
	for name, values := range map[string][]string{
		"a bare revision":       {`"7"`},
		"unquoted":              {strings.Trim(good, `"`)},
		"an uppercase token":    {`"` + strings.ToUpper(thisOpenHex()) + `.7"`},
		"a 15-character token":  {`"` + thisOpenHex()[1:] + `.7"`},
		"a leading zero":        {`"` + thisOpenHex() + `.07"`},
		"2^64":                  {`"` + thisOpenHex() + `.18446744073709551616"`},
		"a star":                {"*"},
		"a weak tag":            {"W/" + good},
		"a list on one line":    {good + ", " + good},
		"two lines":             {good, good},
		"an empty tag":          {`""`},
		"an empty value":        {""},
		"one quote":             {`"`},
		"an open quote":         {strings.TrimSuffix(good, `"`)},
		"an apostrophe opening": {"'" + strings.TrimPrefix(good, `"`)},
		"an apostrophe closing": {strings.TrimSuffix(good, `"`) + "'"},
		"a quote inside":        {`"` + thisOpenHex() + `."7"`},
		"a space inside quotes": {`" ` + strings.Trim(good, `"`) + `"`},
	} {
		t.Run(name, func(t *testing.T) {
			s := newNoteStore(t, ifMatchDoc(t))
			rec := serveIfMatch(s.api, http.MethodPut, "/api/notes/a", `{"text":"x"}`, values...)
			if env := envelopeOf(t, rec); rec.Code != http.StatusBadRequest || env.Code != "if_match_invalid" {
				t.Errorf("If-Match %q answered %d %q, want 400 if_match_invalid", values, rec.Code, env.Code)
			}
			if len(s.got) != 0 {
				t.Errorf("handler ran with %v", s.got)
			}
		})
	}
}

func thisOpenHex() string { return hex.EncodeToString(thisOpen[:]) }

func TestIfMatchMissingIsRequired(t *testing.T) {
	s := newNoteStore(t, ifMatchDoc(t))
	rec := serveIfMatch(s.api, http.MethodPut, "/api/notes/a", `{"text":"x"}`)
	if env := envelopeOf(t, rec); rec.Code != http.StatusPreconditionRequired || env.Code != "if_match_required" {
		t.Errorf("no If-Match answered %d %q, want 428 if_match_required", rec.Code, env.Code)
	}
	if len(s.got) != 0 {
		t.Errorf("handler ran with %v", s.got)
	}
}

func TestIfMatchIsReadAfterTheBody(t *testing.T) {
	s := newNoteStore(t, ifMatchDoc(t))
	rec := serveIfMatch(s.api, http.MethodPut, "/api/notes/a", `{"text":7}`)
	if env := envelopeOf(t, rec); env.Code != "bad_body" {
		t.Errorf("a bad body with no If-Match answered %d %q, want bad_body", rec.Code, env.Code)
	}
	rec = serveIfMatch(s.api, http.MethodPut, "/api/notes/"+strings.Repeat("a", 17), `{"text":"x"}`)
	if env := envelopeOf(t, rec); env.Code != "not_found" {
		t.Errorf("a bad path with no If-Match answered %d %q, want not_found", rec.Code, env.Code)
	}
}

func TestIfMatchIgnoredOnOtherRows(t *testing.T) {
	s := newNoteStore(t, ifMatchDoc(t))
	base := httpwire.Tag{Open: otherOpen, Rev: 3}
	rec := serveIfMatch(s.api, http.MethodPost, "/api/notes/a/merge", `{"base":"`+base.String()+`"}`, "*")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("a row without IfMatch answered %d %q", rec.Code, rec.Body.String())
	}
	if len(s.got) != 2 || s.got[0] != (httpwire.Tag{}) || s.got[1] != base {
		t.Errorf("handler saw IfMatch and Base %v, want the zero tag and %v", s.got, base)
	}
}

func TestTagInABodyRefusedOffItsGrammar(t *testing.T) {
	for name, body := range map[string]string{
		"a bare revision": `{"base":"7"}`,
		"uppercase":       `{"base":"` + strings.ToUpper(thisOpenHex()) + `.7"}`,
		"a number":        `{"base":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newNoteStore(t, ifMatchDoc(t))
			rec := serve(s.api, http.MethodPost, "/api/notes/a/merge", body)
			env := envelopeOf(t, rec)
			if rec.Code != http.StatusBadRequest || env.Code != "bad_body" || !slices.Equal(fieldCodes(env), []string{"base=malformed"}) {
				t.Errorf("answered %d %q %v, want bad_body with base=malformed", rec.Code, env.Code, fieldCodes(env))
			}
			if len(s.got) != 0 {
				t.Errorf("handler ran with %v", s.got)
			}
		})
	}
}

func TestNewMatchesTheIfMatchHeader(t *testing.T) {
	edit := func(t *testing.T, f func(paths map[string]any)) []byte {
		t.Helper()
		var doc map[string]any
		if err := json.Unmarshal(ifMatchDoc(t), &doc); err != nil {
			t.Fatal(err)
		}
		f(doc["paths"].(map[string]any))
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	op := func(paths map[string]any, path, method string) map[string]any {
		return paths[path].(map[string]any)[method].(map[string]any)
	}
	params := func(o map[string]any) []any { return o["parameters"].([]any) }
	ifMatchParam := func(name string, required bool) map[string]any {
		return map[string]any{"in": "header", "name": name, "required": required, "schema": map[string]any{"type": "string"}}
	}
	for name, tc := range map[string]struct {
		edit func(paths map[string]any)
		want []string
	}{
		"no If-Match parameter": {func(paths map[string]any) {
			o := op(paths, "/api/notes/{id}", "put")
			o["parameters"] = params(o)[:1]
		}, []string{`row "notes.save"`, "If-Match", "header"}},
		"an optional If-Match": {func(paths map[string]any) {
			o := op(paths, "/api/notes/{id}", "put")
			o["parameters"] = []any{params(o)[0], ifMatchParam("If-Match", false)}
		}, []string{`row "notes.save"`, "If-Match", "required"}},
		"If-Match on a row without IfMatch": {func(paths map[string]any) {
			o := op(paths, "/api/notes/{id}/merge", "post")
			o["parameters"] = append(params(o), ifMatchParam("If-Match", true))
		}, []string{`row "notes.merge"`, "If-Match"}},
		"another spelling on a row without IfMatch": {func(paths map[string]any) {
			o := op(paths, "/api/notes/{id}/merge", "post")
			o["parameters"] = append(params(o), ifMatchParam("if-match", true))
		}, []string{`row "notes.merge"`, "If-Match"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := httpwire.New(ifMatchTable(), []httpwire.Binding{
				httpwire.Bind("notes.read", func(context.Context, httpwire.Call[noteParams, noQ, noB, noH]) (note, error) { return note{}, nil }),
				httpwire.Bind("notes.save", func(context.Context, httpwire.Call[noteParams, noQ, noteBody, noH]) (httpwire.None, error) {
					return httpwire.None{}, nil
				}),
				httpwire.Bind("notes.merge", func(context.Context, httpwire.Call[noteParams, noQ, mergeBody, noH]) (httpwire.None, error) {
					return httpwire.None{}, nil
				}),
			}, httpwire.WithSchema(edit(t, tc.edit)))
			if err == nil {
				t.Fatal("New accepted the document")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %q", err, w)
				}
			}
		})
	}
	t.Run("another spelling of the header is the header", func(t *testing.T) {
		doc := edit(t, func(paths map[string]any) {
			o := op(paths, "/api/notes/{id}", "put")
			o["parameters"] = []any{params(o)[0], ifMatchParam("if-match", true)}
		})
		newNoteStore(t, doc)
	})
}
