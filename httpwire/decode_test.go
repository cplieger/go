package httpwire_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
	itemKind string
	itemNum  int64
	bigNum   int64
	label    string
	tags     []label
	// keyList is a comma list of lowercase words, parsed by its own
	// UnmarshalText, which names its own field code.
	keyList string
	// parity is an even integer, held by its own UnmarshalJSON.
	parity int64
)

func (itemKind) MaxBytes() int        { return 16 }
func (itemNum) Range() (lo, hi int64) { return 1, 100 }
func (bigNum) Range() (lo, hi int64)  { return 0, 1 << 53 }
func (label) MaxBytes() int           { return 8 }
func (tags) Count() (lo, hi int)      { return 1, 3 }
func (keyList) MaxBytes() int         { return 32 }
func (keyList) Refusal() string       { return "key_list" }
func (k *keyList) UnmarshalText(b []byte) error {
	for w := range strings.SplitSeq(string(b), ",") {
		if w == "" || strings.Trim(w, "abcdefghijklmnopqrstuvwxyz") != "" {
			return errors.New("not a comma list of lowercase words")
		}
	}
	*k = keyList(b)
	return nil
}

func (p *parity) UnmarshalJSON(b []byte) error {
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	if n%2 != 0 {
		return errors.New("odd")
	}
	*p = parity(n)
	return nil
}

type (
	itemParams struct {
		Kind itemKind `json:"kind"`
		N    itemNum  `json:"n"`
	}
	itemQuery struct {
		Kind  *itemKind `json:"kind"`
		Keys  keyList   `json:"keys,omitempty"`
		Limit itemNum   `json:"limit"`
		Big   uint64    `json:"big,omitzero"`
		Flag  bool      `json:"flag,omitempty"`
	}
	itemOut struct {
		Name string `json:"name"`
	}
	shape struct {
		Radius *int64 `json:"radius,omitempty"`
		Side   *int64 `json:"side,omitempty"`
		Type   string `json:"type"`
	}
	itemBody struct {
		Slots  map[int64]label `json:"slots,omitempty"`
		Shape  shape           `json:"shape"`
		Name   label           `json:"name"`
		Kind   itemKind        `json:"kind"`
		Keys   keyList         `json:"keys,omitempty"`
		Tags   tags            `json:"tags"`
		N      itemNum         `json:"n"`
		Copies int64           `json:"copies,string"`
		Size   bigNum          `json:"size"`
		Even   parity          `json:"even,omitempty"`
		Small  int8            `json:"small,omitempty"`
	}
)

func decodeDoc(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/openapi/decode.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// decodeTable is one Public row per decoded part, so no principal option
// stands between a request and its decode.
func decodeTable() *httpwire.Table {
	return &httpwire.Table{
		Prefix: "/api", Catalog: httpwire.NewCatalog(), MaxBody: 512,
		Endpoints: []httpwire.Endpoint{
			{
				Name: "items.get", Method: http.MethodGet, Path: "/api/items/{kind}/{n}", Summary: "Read an item", Description: "Reads one item.",
				Params: reflect.TypeFor[itemParams](), Response: reflect.TypeFor[itemOut](), Access: public,
			},
			{
				Name: "items.file", Method: http.MethodGet, Path: "/api/items/{kind}/{n}/file", Summary: "Download an item",
				Description: "Downloads one item's file.", Params: reflect.TypeFor[itemParams](), Kind: httpwire.Raw, ContentType: "text/plain",
				Access: public,
			},
			{
				Name: "items.list", Method: http.MethodGet, Path: "/api/items", Summary: "List items", Description: "Lists the items.",
				Query: reflect.TypeFor[itemQuery](), Response: reflect.TypeFor[[]itemOut](), Access: public,
			},
			{
				Name: "items.holders", Method: http.MethodGet, Path: "/api/holders", Summary: "Read the holders",
				Description: "Reads who holds each path.", Query: reflect.TypeFor[holdersQuery](), Response: reflect.TypeFor[[]string](),
				Access: public,
			},
			{
				Name: "items.put", Method: http.MethodPost, Path: "/api/items", Summary: "Store an item", Description: "Stores one item.",
				Request: reflect.TypeFor[itemBody](), Access: public,
			},
			{
				Name: "items.import", Method: http.MethodPost, Path: "/api/items/import", Summary: "Import an item",
				Description: "Imports one item, answering its receipt.", Request: reflect.TypeFor[itemBody](), Kind: httpwire.Raw,
				ContentType: "text/plain", Access: public,
			},
		},
	}
}

// decodeAPI serves decodeTable over doc, recording each handler's Call.
type decodeAPI struct {
	api    *httpwire.API
	params *itemParams
	query  *itemQuery
	held   *holdersQuery
	body   *itemBody
	raw    []byte
	file   bool
}

func newDecodeAPI(t *testing.T, doc []byte, opts ...httpwire.Option) *decodeAPI {
	t.Helper()
	d := &decodeAPI{}
	api, err := httpwire.New(decodeTable(), []httpwire.Binding{
		httpwire.Bind("items.get", func(_ context.Context, c httpwire.Call[itemParams, noQ, noB]) (itemOut, error) {
			d.params = &c.Params
			return itemOut{Name: "x"}, nil
		}),
		httpwire.BindRaw("items.file", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			d.file = true
			w.Header().Set("Content-Type", "text/plain")
		})),
		httpwire.Bind("items.list", func(_ context.Context, c httpwire.Call[noP, itemQuery, noB]) ([]itemOut, error) {
			d.query = &c.Query
			return nil, nil
		}),
		httpwire.Bind("items.holders", func(_ context.Context, c httpwire.Call[noP, holdersQuery, noB]) ([]string, error) {
			d.held = &c.Query
			return nil, nil
		}),
		httpwire.Bind("items.put", func(_ context.Context, c httpwire.Call[noP, noQ, itemBody]) (httpwire.None, error) {
			d.body = &c.Body
			return httpwire.None{}, nil
		}),
		httpwire.BindRaw("items.import", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d.raw, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/plain")
		})),
	}, append([]httpwire.Option{httpwire.WithSchema(doc)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.api = api
	return d
}

func (d *decodeAPI) ran() bool {
	return d.params != nil || d.query != nil || d.held != nil || d.body != nil || d.raw != nil || d.file
}

// fieldCodes lists an envelope's fields as sorted "path=code" pairs.
func fieldCodes(env httpwire.Envelope) []string {
	var out []string
	for _, f := range env.Fields {
		out = append(out, f.Path+"="+f.Code)
	}
	slices.Sort(out)
	return out
}

func TestDecodePathFields(t *testing.T) {
	t.Run("a canonical path decodes", func(t *testing.T) {
		d := newDecodeAPI(t, decodeDoc(t))
		rec := serve(d.api, http.MethodGet, "/api/items/disc/100", "")
		if rec.Code != http.StatusOK || d.params == nil || *d.params != (itemParams{Kind: "disc", N: 100}) {
			t.Fatalf("answer %d, Params %+v", rec.Code, d.params)
		}
	})
	for name, path := range map[string]string{
		"a leading zero":             "/api/items/book/07",
		"a plus sign":                "/api/items/book/+7",
		"below Range":                "/api/items/book/0",
		"above Range":                "/api/items/book/101",
		"past int64":                 "/api/items/book/9223372036854775808",
		"not an integer":             "/api/items/book/x",
		"a kind outside the enum":    "/api/items/tape/7",
		"a kind over MaxBytes":       "/api/items/" + strings.Repeat("b", 17) + "/7",
		"a kind of invalid UTF-8":    "/api/items/%FF/7",
		"a Raw row's bad path value": "/api/items/tape/7/file",
	} {
		t.Run(name, func(t *testing.T) {
			d := newDecodeAPI(t, decodeDoc(t))
			rec := serve(d.api, http.MethodGet, path, "")
			if env := envelopeOf(t, rec); rec.Code != http.StatusNotFound || env.Code != "not_found" || len(env.Fields) != 0 {
				t.Errorf("answer %d %s %v, want 404 not_found with no fields", rec.Code, env.Code, env.Fields)
			}
			if d.ran() {
				t.Error("the handler ran")
			}
		})
	}
	t.Run("a Raw row's path is checked before its handler", func(t *testing.T) {
		d := newDecodeAPI(t, decodeDoc(t))
		if rec := serve(d.api, http.MethodGet, "/api/items/book/7/file", ""); rec.Code != http.StatusOK || !d.file {
			t.Errorf("answer %d, handler ran %v", rec.Code, d.file)
		}
	})
}

func TestDecodeQuery(t *testing.T) {
	t.Run("every field type decodes", func(t *testing.T) {
		d := newDecodeAPI(t, decodeDoc(t))
		rec := serve(d.api, http.MethodGet, "/api/items?limit=5&kind=disc&big=18446744073709551615&flag=true&keys=ab,c", "")
		if rec.Code != http.StatusOK || d.query == nil {
			t.Fatalf("answer %d %q", rec.Code, rec.Body.String())
		}
		q := *d.query
		if q.Kind == nil || *q.Kind != "disc" || q.Limit != 5 || q.Big != 1<<64-1 || !q.Flag || q.Keys != "ab,c" {
			t.Errorf("Query = %+v (kind %v)", q, q.Kind)
		}
	})
	t.Run("an optional field left out stays zero", func(t *testing.T) {
		d := newDecodeAPI(t, decodeDoc(t))
		if rec := serve(d.api, http.MethodGet, "/api/items?limit=1", ""); rec.Code != http.StatusOK || d.query == nil || *d.query != (itemQuery{Limit: 1}) {
			t.Errorf("answer %d, Query %+v", rec.Code, d.query)
		}
	})
	for name, tc := range map[string]struct {
		query string
		want  []string
	}{
		"a required key missing":      {"kind=book", []string{"limit=required"}},
		"a key given twice":           {"limit=5&limit=6", []string{"limit=malformed"}},
		"an unknown key":              {"limit=5&zz=1", []string{"zz=unknown"}},
		"a leading zero":              {"limit=05", []string{"limit=malformed"}},
		"a plus sign":                 {"limit=+5", []string{"limit=malformed"}},
		"an empty value":              {"limit=", []string{"limit=malformed"}},
		"below Range":                 {"limit=0", []string{"limit=range"}},
		"above Range":                 {"limit=101", []string{"limit=range"}},
		"past int64":                  {"limit=99999999999999999999", []string{"limit=range"}},
		"a uint64 below zero":         {"limit=5&big=-1", []string{"big=malformed"}},
		"a uint64 with a zero lead":   {"limit=5&big=01", []string{"big=malformed"}},
		"past uint64":                 {"limit=5&big=18446744073709551616", []string{"big=range"}},
		"a bool in another spelling":  {"limit=5&flag=True", []string{"flag=malformed"}},
		"a bool as a digit":           {"limit=5&flag=1", []string{"flag=malformed"}},
		"a value outside its enum":    {"limit=5&kind=tape", []string{"kind=enum"}},
		"a string over MaxBytes":      {"limit=5&kind=" + strings.Repeat("b", 17), []string{"kind=range"}},
		"invalid UTF-8":               {"limit=5&kind=%FF", []string{"kind=malformed"}},
		"a text its type refuses":     {"limit=5&keys=A", []string{"keys=key_list"}},
		"a text over its MaxBytes":    {"limit=5&keys=" + strings.Repeat("a", 33), []string{"keys=key_list"}},
		"a query that does not parse": {"limit=5;x", []string{"=malformed"}},
		"each fault at once":          {"limit=0&flag=no&zz=1", []string{"flag=malformed", "limit=range", "zz=unknown"}},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDecodeAPI(t, decodeDoc(t))
			rec := serve(d.api, http.MethodGet, "/api/items?"+tc.query, "")
			env := envelopeOf(t, rec)
			if rec.Code != http.StatusBadRequest || env.Code != "bad_query" || !slices.Equal(fieldCodes(env), tc.want) {
				t.Errorf("answer %d %s %v, want 400 bad_query %v", rec.Code, env.Code, fieldCodes(env), tc.want)
			}
			if d.ran() {
				t.Error("the handler ran")
			}
		})
	}
}

const validItem = `{"name":"a","kind":"book","n":1,"copies":"5","size":9007199254740992,"tags":["x"],"shape":{"type":"circle","radius":2}}`

// withItem answers validItem with one member set to raw, or removed when raw
// is empty.
func withItem(t *testing.T, member, raw string) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(validItem), &m); err != nil {
		t.Fatal(err)
	}
	if raw == "" {
		delete(m, member)
	} else {
		m[member] = json.RawMessage(raw)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDecodeBody(t *testing.T) {
	t.Run("a valid body decodes", func(t *testing.T) {
		d := newDecodeAPI(t, decodeDoc(t))
		body := withItem(t, "slots", `{"-3":"s"}`)
		body = strings.Replace(body, "{", `{"even":4,"small":-128,`, 1)
		rec := serve(d.api, http.MethodPost, "/api/items", body)
		if rec.Code != http.StatusNoContent || d.body == nil {
			t.Fatalf("answer %d %q", rec.Code, rec.Body.String())
		}
		b := d.body
		if b.Name != "a" || b.Kind != "book" || b.N != 1 || b.Copies != 5 || b.Size != 1<<53 || !slices.Equal(b.Tags, tags{"x"}) ||
			b.Shape.Type != "circle" || b.Shape.Radius == nil || *b.Shape.Radius != 2 || b.Slots[-3] != "s" || b.Even != 4 || b.Small != -128 {
			t.Errorf("Body = %+v", *b)
		}
	})
	t.Run("a body of exactly MaxBody bytes decodes", func(t *testing.T) {
		d := newDecodeAPI(t, decodeDoc(t))
		body := validItem + strings.Repeat(" ", 512-len(validItem))
		if rec := serve(d.api, http.MethodPost, "/api/items", body); rec.Code != http.StatusNoContent {
			t.Errorf("answer %d %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("a body a byte over MaxBody answers body_too_large", func(t *testing.T) {
		d := newDecodeAPI(t, decodeDoc(t))
		body := validItem + strings.Repeat(" ", 513-len(validItem))
		rec := serve(d.api, http.MethodPost, "/api/items", body)
		if env := envelopeOf(t, rec); rec.Code != http.StatusRequestEntityTooLarge || env.Code != "body_too_large" {
			t.Errorf("answer %d %s", rec.Code, env.Code)
		}
		if d.ran() {
			t.Error("the handler ran")
		}
	})
	for name, tc := range map[string]struct {
		body string
		want []string
		// schemaToo marks a case CheckBody answers alike on the same bytes.
		schemaToo bool
	}{
		"an empty body":                      {"", []string{"=required"}, true},
		"not JSON":                           {"{", []string{"=malformed"}, true},
		"two values":                         {validItem + " {}", []string{"=malformed"}, true},
		"not an object":                      {"[]", []string{"=malformed"}, true},
		"an unknown field":                   {withItem(t, "zz", "1"), []string{"zz=unknown"}, true},
		"a required field sent as null":      {withItem(t, "name", "null"), []string{"name=required"}, true},
		"a required field missing":           {withItem(t, "name", ""), []string{"name=required"}, true},
		"a string a byte over MaxBytes":      {withItem(t, "name", `"abcdefghi"`), []string{"name=range"}, true},
		"a value outside its enum":           {withItem(t, "kind", `"tape"`), []string{"kind=enum"}, true},
		"a field of another union case":      {withItem(t, "shape", `{"type":"circle","radius":1,"side":2}`), []string{"shape.side=union"}, true},
		"a union tag naming no case":         {withItem(t, "shape", `{"type":"oval","radius":1}`), []string{"shape.type=union"}, true},
		"a ,string integer off its grammar":  {withItem(t, "copies", `"5x"`), []string{"copies=pattern"}, true},
		"an integer map key that is not one": {withItem(t, "slots", `{"x":"s"}`), []string{"slots.x=pattern"}, true},
		"an integer a unit over Range":       {withItem(t, "size", "9007199254740993"), []string{"size=range"}, true},
		"a fraction under integer":           {withItem(t, "n", "1.5"), []string{"n=malformed"}, true},
		"an integer below Range":             {withItem(t, "n", "0"), []string{"n=range"}, true},
		"a wrong JSON type":                  {withItem(t, "n", `"1"`), []string{"n=malformed"}, true},
		"a null under a Count from 1":        {withItem(t, "tags", "null"), []string{"tags=required"}, true},
		"too few items":                      {withItem(t, "tags", "[]"), []string{"tags=range"}, true},
		"too many items":                     {withItem(t, "tags", `["a","b","c","d"]`), []string{"tags=range"}, true},
		"an item over MaxBytes":              {withItem(t, "tags", `["a","abcdefghi"]`), []string{"tags[1]=range"}, true},
		"a text its type refuses":            {withItem(t, "keys", `"A"`), []string{"keys=key_list"}, false},
		"a value its own decoder refuses":    {withItem(t, "even", "3"), []string{"even=malformed"}, false},
		"an int8 past its range":             {withItem(t, "small", "128"), []string{"small=range"}, true},
		"a text over its MaxBytes":           {withItem(t, "keys", `"`+strings.Repeat("a", 33)+`"`), []string{"keys=key_list"}, false},
		"a name in another letter case":      {strings.Replace(validItem, `"name"`, `"NAME"`, 1), []string{"NAME=unknown", "name=required"}, true},
		"each fault at once":                 {`{"name":"abcdefghi","kind":"tape","n":0,"copies":"5","size":0,"tags":[],"shape":{"type":"square","side":1}}`, []string{"kind=enum", "n=range", "name=range", "tags=range"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDecodeAPI(t, decodeDoc(t))
			rec := serve(d.api, http.MethodPost, "/api/items", tc.body)
			env := envelopeOf(t, rec)
			if rec.Code != http.StatusBadRequest || env.Code != "bad_body" || !slices.Equal(fieldCodes(env), tc.want) {
				t.Errorf("answer %d %s %v, want 400 bad_body %v", rec.Code, env.Code, fieldCodes(env), tc.want)
			}
			if d.ran() {
				t.Error("the handler ran")
			}
			if !tc.schemaToo {
				return
			}
			s, err := httpwire.ParseSchema(decodeDoc(t))
			if err != nil {
				t.Fatal(err)
			}
			if got := fieldCodes(httpwire.Envelope{Fields: s.CheckBody("items.put", 0, []byte(tc.body))}); !slices.Equal(got, tc.want) {
				t.Errorf("CheckBody on the same bytes = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDecodeHandsARawRowItsCheckedBody(t *testing.T) {
	t.Run("a valid body reaches the handler whole", func(t *testing.T) {
		d := newDecodeAPI(t, decodeDoc(t))
		if rec := serve(d.api, http.MethodPost, "/api/items/import", validItem); rec.Code != http.StatusOK || string(d.raw) != validItem {
			t.Errorf("answer %d, the handler read %q", rec.Code, d.raw)
		}
	})
	t.Run("a refused body runs no handler", func(t *testing.T) {
		d := newDecodeAPI(t, decodeDoc(t))
		rec := serve(d.api, http.MethodPost, "/api/items/import", withItem(t, "kind", `"tape"`))
		if env := envelopeOf(t, rec); env.Code != "bad_body" || !slices.Equal(fieldCodes(env), []string{"kind=enum"}) || d.ran() {
			t.Errorf("answer %d %s %v, handler ran %v", rec.Code, env.Code, fieldCodes(env), d.ran())
		}
	})
}

// openItemDoc is the decode document stating none of the types' bounds,
// A body the binder decodes is JSON (RFC 8259 section 11), so a request
// naming another media type, or none, answers 415 before the body is read;
// a Raw row that reads its own body takes whatever type it is sent.
func TestBodyMediaTypeRefusals(t *testing.T) {
	post := func(api http.Handler, target, body, ct string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, r)
		return rec
	}
	for name, ct := range map[string]string{
		"text/plain":                   "text/plain",
		"a form":                       "application/x-www-form-urlencoded",
		"no Content-Type":              "",
		"another charset":              "application/json; charset=latin1",
		"an unparsable type":           "application/json; charset",
		"a type the JSON one prefixes": "application/jsonl",
		"a structured suffix":          "application/json-patch+json",
	} {
		for _, target := range []string{"/api/items", "/api/items/import"} {
			t.Run(name+" "+target, func(t *testing.T) {
				d := newDecodeAPI(t, decodeDoc(t))
				rec := post(d.api, target, validItem, ct)
				if env := envelopeOf(t, rec); rec.Code != http.StatusUnsupportedMediaType || env.Code != "unsupported_media_type" || d.ran() {
					t.Errorf("answer %d %s, handler ran %v; want 415 unsupported_media_type and no run", rec.Code, env.Code, d.ran())
				}
			})
		}
	}
	for name, ct := range map[string]string{
		"application/json":  "application/json",
		"another case":      "Application/JSON",
		"utf-8":             "application/json; charset=utf-8",
		"UTF-8":             "application/json; charset=UTF-8",
		"another parameter": "application/json; v=1",
	} {
		t.Run("passes "+name, func(t *testing.T) {
			d := newDecodeAPI(t, decodeDoc(t))
			if rec := post(d.api, "/api/items", validItem, ct); rec.Code != http.StatusNoContent || d.body == nil {
				t.Errorf("answer %d %q, want the body decoded", rec.Code, rec.Body)
			}
		})
	}
	t.Run("an empty JSON body is still a missing body", func(t *testing.T) {
		d := newDecodeAPI(t, decodeDoc(t))
		if env := envelopeOf(t, post(d.api, "/api/items", "", "application/json")); env.Code != "bad_body" || !slices.Equal(fieldCodes(env), []string{"=required"}) {
			t.Errorf("answered %s %v, want bad_body =required", env.Code, fieldCodes(env))
		}
	})
	t.Run("a Raw row with no declared body", func(t *testing.T) {
		sf := newSpool(t)
		if rec := sf.send("plain text", `"k"`, 0); rec.Code != http.StatusOK || sf.runs() != 1 {
			t.Errorf("answer %d %q after %d runs, want the handler run", rec.Code, rec.Body, sf.runs())
		}
	})
	t.Run("before the idempotency key", func(t *testing.T) {
		kf := newKeyed(t, httpwire.IdempotencyOptions{}, nil)
		r := keyedRequest(http.MethodPost, "/api/jobs/quick", `{"kind":"a","size":1}`, "")
		r.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		kf.api.ServeHTTP(rec, r)
		if codeOf(t, rec) != "unsupported_media_type" || kf.runsOf("jobs.quick") != 0 {
			t.Errorf("a keyless text/plain request answered %s after %d runs, want unsupported_media_type", codeOf(t, rec), kf.runsOf("jobs.quick"))
		}
	})
}

// openItemDoc is the decode document stating none of the types' bounds,
// with the body open.
func openItemDoc(t *testing.T) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(decodeDoc(t), &doc); err != nil {
		t.Fatal(err)
	}
	plainInt := map[string]any{"type": "integer"}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	body := schemas["ItemBody"].(map[string]any)
	body["additionalProperties"] = map[string]any{}
	props := body["properties"].(map[string]any)
	props["name"] = map[string]any{"type": "string"}
	props["size"], props["small"] = plainInt, plainInt
	props["tags"] = map[string]any{"type": []any{"array", "null"}, "items": map[string]any{"type": "string"}}
	paths := doc["paths"].(map[string]any)
	paths["/api/items/{kind}/{n}"].(map[string]any)["get"].(map[string]any)["parameters"].([]any)[1].(map[string]any)["schema"] = plainInt
	for _, p := range paths["/api/items"].(map[string]any)["get"].(map[string]any)["parameters"].([]any) {
		if p.(map[string]any)["name"] == "limit" {
			p.(map[string]any)["schema"] = plainInt
		}
	}
	plain, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

// TestDecodeHoldsBoundsTheSchemaLacks serves a document that states none of
// the types' bounds and leaves the body open: the decode refuses on its own.
func TestDecodeHoldsBoundsTheSchemaLacks(t *testing.T) {
	plain := openItemDoc(t)
	for name, tc := range map[string]struct {
		member, raw, want string
	}{
		"MaxBytes":                    {"name", `"abcdefghi"`, "name=range"},
		"Range":                       {"size", "9007199254740993", "size=range"},
		"Count":                       {"tags", "[]", "tags=range"},
		"a null under a Count from 1": {"tags", "null", "tags=required"},
		"an item's MaxBytes":          {"tags", `["abcdefghi"]`, "tags[0]=range"},
		"an unknown field":            {"zz", "1", "zz=unknown"},
		"an int8 past its range":      {"small", "300", "small=malformed"},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDecodeAPI(t, plain)
			rec := serve(d.api, http.MethodPost, "/api/items", withItem(t, tc.member, tc.raw))
			if env := envelopeOf(t, rec); env.Code != "bad_body" || !slices.Equal(fieldCodes(env), []string{tc.want}) {
				t.Errorf("answer %d %s %v, want bad_body [%s]", rec.Code, env.Code, fieldCodes(env), tc.want)
			}
		})
	}
	t.Run("a query value's Range", func(t *testing.T) {
		d := newDecodeAPI(t, plain)
		rec := serve(d.api, http.MethodGet, "/api/items?limit=101", "")
		if env := envelopeOf(t, rec); env.Code != "bad_query" || !slices.Equal(fieldCodes(env), []string{"limit=range"}) {
			t.Errorf("answer %d %s %v, want bad_query [limit=range]", rec.Code, env.Code, fieldCodes(env))
		}
	})
	for _, n := range []string{"0", "101"} {
		t.Run("a path value's Range "+n, func(t *testing.T) {
			d := newDecodeAPI(t, plain)
			if rec := serve(d.api, http.MethodGet, "/api/items/book/"+n, ""); rec.Code != http.StatusNotFound || d.ran() {
				t.Errorf("answer %d, handler ran %v", rec.Code, d.ran())
			}
		})
	}
}

// TestBodyRefusalsNameNoGoInternals sends bodies whose decode fails in
// encoding/json, in a type's own decoder and in the schema check: no
// refusal's text names the server's Go types or a decoder's own error.
func TestBodyRefusalsNameNoGoInternals(t *testing.T) {
	internals := []string{"Go ", "struct", "json", "itemBody", "httpwire", "cannot unmarshal", "invalid character", "unexpected", "odd", "comma list"}
	closed, open := decodeDoc(t), openItemDoc(t)
	for name, tc := range map[string]struct {
		doc  []byte
		body string
	}{
		"a string under an integer":         {closed, withItem(t, "n", `"1"`)},
		"an object under a list":            {closed, withItem(t, "tags", `{}`)},
		"a text its type refuses":           {closed, withItem(t, "keys", `"A"`)},
		"a value its own decoder refuses":   {closed, withItem(t, "even", "3")},
		"a syntax error":                    {closed, `{"name":}`},
		"a cut body":                        {closed, `{"name":"a"`},
		"two values":                        {closed, validItem + " {}"},
		"past a Go type the document lacks": {open, withItem(t, "small", "300")},
	} {
		t.Run(name, func(t *testing.T) {
			rec := serve(newDecodeAPI(t, tc.doc).api, http.MethodPost, "/api/items", tc.body)
			env := envelopeOf(t, rec)
			if env.Code != "bad_body" || len(env.Fields) == 0 {
				t.Fatalf("answer %d %s %v, want bad_body with fields", rec.Code, env.Code, env.Fields)
			}
			text := env.Message
			for _, f := range env.Fields {
				text += " | " + f.Message
			}
			for _, word := range internals {
				if strings.Contains(text, word) {
					t.Errorf("refusal %q names %q", text, word)
				}
			}
		})
	}
}

// TestBodyDecoderErrorIsLoggedAtDebug keeps the decoder's own text, which
// never crosses the wire, in a DEBUG line naming the row.
func TestBodyDecoderErrorIsLoggedAtDebug(t *testing.T) {
	for name, tc := range map[string]struct{ body, text string }{
		"the JSON decoder's refusal": {withItem(t, "n", `"1"`), "cannot unmarshal"},
		"a type's own decoder":       {withItem(t, "even", "3"), "odd"},
	} {
		t.Run(name, func(t *testing.T) {
			sink := &logSink{}
			d := newDecodeAPI(t, decodeDoc(t), httpwire.WithLogger(slog.New(sink)))
			if rec := serve(d.api, http.MethodPost, "/api/items", tc.body); rec.Code != http.StatusBadRequest {
				t.Fatalf("answer %d %s", rec.Code, rec.Body)
			}
			if !sink.has("DEBUG", "row=items.put", tc.text) {
				t.Errorf("no DEBUG line naming the row and %q: %q", tc.text, sink.all())
			}
		})
	}
	t.Run("an accepted body logs nothing", func(t *testing.T) {
		sink := &logSink{}
		d := newDecodeAPI(t, decodeDoc(t), httpwire.WithLogger(slog.New(sink)))
		if rec := serve(d.api, http.MethodPost, "/api/items", validItem); rec.Code != http.StatusNoContent || len(sink.all()) != 0 {
			t.Errorf("answer %d, lines %q", rec.Code, sink.all())
		}
	})
}
