package jsonx_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/go/jsonx"
)

// widget / part form the small schema the parity tests decode both ways: a
// bounded token walk must be observably identical to json.Unmarshal on any
// input both accept.
type widget struct {
	Name   string                       `json:"name"`
	Count  int                          `json:"count"`
	Tags   []string                     `json:"tags"`
	Parts  []part                       `json:"parts"`
	Meta   map[string]string            `json:"meta"`
	Specs  map[string]part              `json:"specs"`
	Nested map[string]map[string]string `json:"nested"`
}

type part struct {
	ID   int    `json:"id"`
	Kind string `json:"kind"`
}

// decodeWidget is the reference consumer shape: Object walk, EqualFold
// dispatch, Array for slices and Map for maps (both with prior for
// duplicate-key parity), Skip for unknown fields.
func decodeWidget(d *jsonx.Decoder, w *widget, tagCap, partCap int) error {
	return d.Object(func(k string) error {
		switch {
		case strings.EqualFold(k, "name"):
			return d.Decode(&w.Name)
		case strings.EqualFold(k, "count"):
			return d.Decode(&w.Count)
		case strings.EqualFold(k, "tags"):
			var err error
			w.Tags, err = d.Array(w.Tags, tagCap, "tags", func(s *string) error { return d.Decode(s) })
			return err
		case strings.EqualFold(k, "parts"):
			var err error
			w.Parts, err = d.Array(w.Parts, partCap, "parts", func(p *part) error { return decodePart(d, p) })
			return err
		case strings.EqualFold(k, "meta"):
			var err error
			w.Meta, err = d.Map(w.Meta, tagCap, "meta", func(_ string, v *string) error { return d.Decode(v) })
			return err
		case strings.EqualFold(k, "specs"):
			var err error
			w.Specs, err = d.Map(w.Specs, partCap, "specs", func(_ string, v *part) error { return decodePart(d, v) })
			return err
		case strings.EqualFold(k, "nested"):
			var err error
			w.Nested, err = d.Map(w.Nested, tagCap, "nested", func(key string, v *map[string]string) error {
				var inner error
				*v, inner = d.Map(*v, tagCap, "nested."+key, func(_ string, s *string) error { return d.Decode(s) })
				return inner
			})
			return err
		default:
			return d.Skip()
		}
	})
}

func decodePart(d *jsonx.Decoder, p *part) error {
	return d.Object(func(k string) error {
		switch {
		case strings.EqualFold(k, "id"):
			return d.Decode(&p.ID)
		case strings.EqualFold(k, "kind"):
			return d.Decode(&p.Kind)
		default:
			return d.Skip()
		}
	})
}

// budgetWidget decodes body through the bounded walk with the given caps
// and budget, including the End trailing-data check (matching
// json.Unmarshal's whole-input strictness).
func budgetWidget(body []byte, tagCap, partCap, budget int) (widget, error) {
	d := jsonx.NewDecoder(bytes.NewReader(body), budget)
	var w widget
	if err := decodeWidget(d, &w, tagCap, partCap); err != nil {
		return widget{}, err
	}
	if err := d.End(); err != nil {
		return widget{}, err
	}
	return w, nil
}

func TestParityWithUnmarshal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{name: "full document", body: `{"name":"a","count":2,"tags":["x","y"],"parts":[{"id":1,"kind":"k"},{"id":2}]}`},
		{name: "case-variant keys", body: `{"NAME":"a","Count":2,"TAGS":["x"],"PaRtS":[{"ID":7,"KIND":"k"}]}`},
		{name: "unknown fields skipped", body: `{"other":{"deep":[1,2,{"x":null}]},"name":"a","junk":[[[]]],"flag":true}`},
		{name: "huge number in skipped field", body: `{"big":1e1000,"name":"a"}`},
		{name: "duplicate scalar last wins", body: `{"count":1,"count":9}`},
		{name: "duplicate array merges fieldwise", body: `{"parts":[{"id":1,"kind":"k"},{"id":2,"kind":"m"}],"parts":[{"id":5}]}`},
		{name: "duplicate array empty replaces", body: `{"tags":["x","y"],"tags":[]}`},
		{name: "duplicate array null nils", body: `{"tags":["x"],"tags":null}`},
		{name: "empty array allocates empty non-nil slice", body: `{"tags":[],"parts":[]}`},
		{name: "null into slice", body: `{"tags":null}`},
		{name: "null array element is no-op", body: `{"parts":[null]}`},
		{name: "top-level null", body: `null`},
		{name: "empty object", body: `{}`},
		{name: "duplicate object key merges", body: `{"name":"a","name":"b","count":3}`},
		{name: "map entries", body: `{"meta":{"a":"1","b":"2"}}`},
		{name: "map with struct values", body: `{"specs":{"x":{"id":1,"kind":"k"},"y":{"id":2}}}`},
		{name: "map keys are case-sensitive, unlike struct fields", body: `{"meta":{"a":"1","A":"2"}}`},
		{name: "duplicate map key replaces with a fresh zero", body: `{"specs":{"x":{"id":1,"kind":"k"},"x":{"id":2}}}`},
		{name: "duplicate map field merges entries", body: `{"meta":{"a":"1"},"meta":{"b":"2"}}`},
		{name: "duplicate map field overwrites a repeated entry", body: `{"meta":{"a":"1"},"meta":{"a":"2"}}`},
		{name: "null into map leaves it untouched", body: `{"meta":{"a":"1"},"meta":null}`},
		{name: "null map field allocates nothing", body: `{"meta":null}`},
		{name: "empty object allocates an empty non-nil map", body: `{"meta":{}}`},
		{name: "null map value stores the zero value", body: `{"meta":{"a":null},"specs":{"x":null}}`},
		{name: "nested map", body: `{"nested":{"outer":{"a":"1","b":"2"},"other":{"c":"3"}}}`},
		{name: "nested empty and null maps", body: `{"nested":{"empty":{},"null":null}}`},
		{name: "duplicate nested map key replaces the inner map", body: `{"nested":{"o":{"a":"1"},"o":{"b":"2"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var want widget
			if err := json.Unmarshal([]byte(tc.body), &want); err != nil {
				t.Fatalf("json.Unmarshal: %v (parity cases must be stdlib-accepted)", err)
			}
			got, err := budgetWidget([]byte(tc.body), 0, 0, 0)
			if err != nil {
				t.Fatalf("bounded decode: %v", err)
			}
			// Exact DeepEqual, no normalization: nil-vs-empty parity is part
			// of the contract (null → nil, `[]` → empty non-nil, absent →
			// untouched), exactly as json.Unmarshal behaves.
			if !reflect.DeepEqual(got, want) {
				t.Errorf("bounded = %+v, want json.Unmarshal parity %+v", got, want)
			}
		})
	}
}

func TestTrailingDataRejected(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{} {}`, `{"name":"a"} x`, `null 1`} {
		if _, err := budgetWidget([]byte(body), 0, 0, 0); err == nil {
			t.Errorf("budgetWidget(%q) = nil error, want trailing-data rejection", body)
		}
		var w widget
		if err := json.Unmarshal([]byte(body), &w); err == nil {
			t.Errorf("json.Unmarshal(%q) = nil error; parity case is stale", body)
		}
	}
}

func TestArrayCapRejects(t *testing.T) {
	t.Parallel()
	body := []byte(`{"tags":["a","b","c","d"]}`)
	_, err := budgetWidget(body, 3, 0, 0)
	if !errors.Is(err, jsonx.ErrArrayCap) {
		t.Fatalf("err = %v, want ErrArrayCap", err)
	}
	if !strings.Contains(err.Error(), "tags") {
		t.Errorf("err = %q, want the array named via what", err)
	}
	if _, err := budgetWidget(body, 4, 0, 0); err != nil {
		t.Errorf("at-cap decode = %v, want nil", err)
	}
}

func TestElementBudgetAggregatesAcrossArrays(t *testing.T) {
	t.Parallel()
	// 3 tags + 2 parts = 5 elements against the aggregate budget, whichever
	// array they belong to.
	body := []byte(`{"tags":["a","b","c"],"parts":[{"id":1},{"id":2}]}`)
	if _, err := budgetWidget(body, 0, 0, 4); !errors.Is(err, jsonx.ErrElementBudget) {
		t.Fatalf("budget 4: err = %v, want ErrElementBudget", err)
	}
	if _, err := budgetWidget(body, 0, 0, 5); err != nil {
		t.Errorf("budget 5: err = %v, want nil", err)
	}
	d := jsonx.NewDecoder(bytes.NewReader(body), 0)
	var w widget
	if err := decodeWidget(d, &w, 0, 0); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := d.Elements(); got != 5 {
		t.Errorf("Elements() = %d, want 5", got)
	}
}

func TestArrayCapCheckedBeforeBudgetAndDecode(t *testing.T) {
	t.Parallel()
	// The 4th element crosses the per-array cap of 3: the cap must reject
	// before the element is charged or decoded, so the error is ErrArrayCap
	// even though the budget (also 3) would have tripped on the same element.
	body := []byte(`{"tags":["a","b","c","d"]}`)
	_, err := budgetWidget(body, 3, 0, 3)
	if !errors.Is(err, jsonx.ErrArrayCap) {
		t.Fatalf("err = %v, want the per-array cap to fire before the budget charge", err)
	}
}

func TestOpen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		body    string
		wantOK  bool
		wantErr bool
	}{
		{name: "object opens", body: `{}`, wantOK: true},
		{name: "null reports not-ok without error", body: `null`},
		{name: "wrong delimiter errors", body: `[]`, wantErr: true},
		{name: "scalar errors", body: `5`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := jsonx.NewDecoder(strings.NewReader(tc.body), 0)
			ok, err := d.Open(json.Delim('{'))
			if ok != tc.wantOK || (err != nil) != tc.wantErr {
				t.Errorf("Open = (%v, %v), want ok=%v wantErr=%v", ok, err, tc.wantOK, tc.wantErr)
			}
		})
	}
}

func TestSkipConsumesWholeValue(t *testing.T) {
	t.Parallel()
	// After skipping the first key's nested value, the walk must land
	// exactly on the next key.
	d := jsonx.NewDecoder(strings.NewReader(`{"skip":{"a":[1,{"b":2}],"c":"d"},"name":"kept"}`), 0)
	var w widget
	if err := decodeWidget(d, &w, 0, 0); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if w.Name != "kept" {
		t.Errorf("Name = %q, want the key after the skipped container decoded", w.Name)
	}
}

func TestObjectNullLeavesTargetUntouched(t *testing.T) {
	t.Parallel()
	d := jsonx.NewDecoder(strings.NewReader(`null`), 0)
	p := part{ID: 7, Kind: "k"}
	if err := decodePart(d, &p); err != nil {
		t.Fatalf("decodePart on null: %v", err)
	}
	if p.ID != 7 || p.Kind != "k" {
		t.Errorf("part = %+v, want pre-decoded value untouched by null", p)
	}
}

func TestDuplicateArrayRegrowReExposesBacking(t *testing.T) {
	t.Parallel()
	// First occurrence decodes two full parts; the duplicate decodes one
	// partial part INTO the retained backing element, so the un-overwritten
	// field survives (stdlib duplicate-key merge) and the length truncates.
	body := []byte(`{"parts":[{"id":1,"kind":"k"},{"id":2,"kind":"m"}],"parts":[{"id":5}]}`)
	got, err := budgetWidget(body, 0, 0, 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []part{{ID: 5, Kind: "k"}}
	if !reflect.DeepEqual(got.Parts, want) {
		t.Errorf("Parts = %+v, want %+v (merge into retained backing, truncated)", got.Parts, want)
	}
}

// TestPreflightAcceptsUnambiguousBodies pins that the preflight is a gate on
// STRUCTURE only: every shape a schema decoder legitimately meets - nesting,
// repeated keys in SIBLING objects, keys differing by more than case - passes
// untouched.
func TestPreflightAcceptsUnambiguousBodies(t *testing.T) {
	t.Parallel()
	bodies := []string{
		`null`,
		`1`,
		`"s"`,
		`{}`,
		`[]`,
		`{"name":"a","count":2}`,
		`{"a":{"b":{"c":[1,2,{"d":null}]}}}`,
		`[{"id":1},{"id":1},{"id":1}]`,
		`{"outer":{"id":1},"other":{"id":2}}`,
		`{"id":1,"ID_":2}`,
		`{"":1}`,
		// UseNumber means an out-of-float64-range literal is structurally fine,
		// matching json.Unmarshal's own field skipping.
		`{"big":1e1000}`,
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			if err := jsonx.Preflight(strings.NewReader(body)); err != nil {
				t.Errorf("Preflight(%q) = %v, want it accepted", body, err)
			}
			if !json.Valid([]byte(body)) {
				t.Errorf("json.Valid(%q) = false; the acceptance case is stale", body)
			}
		})
	}
}

// TestPreflightRejectsDuplicateKeys pins the fail-closed half: the repeat is
// rejected wherever it sits, and the match is case-insensitive because
// encoding/json resolves struct fields case-insensitively, so the two
// spellings address one field and are equally ambiguous. Each body is
// cross-checked against json.Unmarshal to prove the stdlib ACCEPTS it - the
// silent tolerance being closed here.
func TestPreflightRejectsDuplicateKeys(t *testing.T) {
	t.Parallel()
	bodies := []string{
		`{"name":"a","name":"b"}`,
		`{"tags":["x"],"tags":null}`,
		`{"NAME":"a","name":"b"}`,
		`{"Name":"a","nAMe":"b"}`,
		`{"":1,"":2}`,
		`{"outer":{"id":1,"ID":2}}`,
		`[{"ok":1},{"id":1,"id":2}]`,
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			err := jsonx.Preflight(strings.NewReader(body))
			if !errors.Is(err, jsonx.ErrDuplicateKey) {
				t.Errorf("Preflight(%q) = %v, want ErrDuplicateKey", body, err)
			}
			// The stdlib ACCEPTS every one of these, resolving the repeat to
			// its last occurrence: that silent tolerance is what the preflight
			// exists to close, so a case the stdlib starts rejecting is stale.
			var v any
			if unmarshalErr := json.Unmarshal([]byte(body), &v); unmarshalErr != nil {
				t.Errorf("json.Unmarshal(%q) = %v; the ambiguity case is stale", body, unmarshalErr)
			}
		})
	}
}

// TestPreflightKeyFoldMatchesEqualFold pins the fold canonicalization against
// the semantics it stands in for: two sibling keys collide exactly when
// strings.EqualFold says they do. The fold exists only to make that test
// O(keys) instead of O(keys^2), so any divergence is a bug in the
// optimization, not a policy choice.
func TestPreflightKeyFoldMatchesEqualFold(t *testing.T) {
	t.Parallel()
	pairs := [][2]string{
		{"k", "K"},
		{"media", "Media"},
		{"s", "S"},
		{"\u017f", "S"}, // LATIN SMALL LETTER LONG S folds to 's'
		{"\u0131", "I"}, // DOTLESS I does NOT fold to ASCII 'i'
		{"\u212a", "K"}, // KELVIN SIGN folds to 'k'
		{"\u0130", "i"}, // LATIN CAPITAL I WITH DOT ABOVE does NOT fold to 'i'
		{"\u00e9", "\u00c9"},
		// The three orbits Unicode 17 changed where both members were already
		// assigned in Unicode 15, so real upstream text can carry them. They
		// are the only keys whose verdict here the toolchain's Unicode table
		// can move, and encoding/json's v1 field matching moves with them, so
		// they are exactly the pairs whose alignment must hold.
		{"\u0390", "\u1fd3"},
		{"\u03b0", "\u1fe3"},
		{"\ufb05", "\ufb06"},
		{"a", "b"},
		{"", "x"},
	}
	for _, p := range pairs {
		t.Run(p[0]+"|"+p[1], func(t *testing.T) {
			t.Parallel()
			body, err := json.Marshal(map[string]int{p[0]: 1})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			second, err := json.Marshal(map[string]int{p[1]: 2})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			// Splice the two single-key objects into one two-key object.
			joined := string(body[:len(body)-1]) + "," + string(second[1:])
			collides := errors.Is(jsonx.Preflight(strings.NewReader(joined)), jsonx.ErrDuplicateKey)
			if want := strings.EqualFold(p[0], p[1]); collides != want {
				t.Errorf("Preflight(%q) collision = %v, want strings.EqualFold = %v", joined, collides, want)
			}
		})
	}
}

// stdlibMaxDepth is encoding/json's nesting ceiling since Go 1.27, written out
// here because the standard library does not export it and jsonx holds no
// copy: the tests below check it against json.Unmarshal, so a toolchain that
// moves it fails them.
const stdlibMaxDepth = 10000

// TestPreflightDepthMatchesUnmarshal pins the depth contract Preflight
// promises: its acceptance set is exactly json.Unmarshal's, so it can never
// admit a body the decode step would reject on depth, nor reject one it
// would accept. The ceiling is encoding/json's, not this package's (jsontext
// refuses to read the token that would open container stdlibMaxDepth+1), so
// both boundary levels are asserted against json.Unmarshal rather than against
// stdlibMaxDepth alone: a future toolchain that moves the stdlib ceiling fails
// this test instead of leaving the constant to drift silently.
//
// The error TEXT is deliberately not pinned: jsontext keeps its depth error
// unexported, so the type is the strongest stable assertion available.
func TestPreflightDepthMatchesUnmarshal(t *testing.T) {
	t.Parallel()
	shapes := map[string]struct {
		nest, unnest, leaf string
	}{
		"arrays":  {nest: "[", unnest: "]"},
		"objects": {nest: `{"a":`, unnest: "}", leaf: "1"},
	}
	for name, shape := range shapes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := func(n int) []byte {
				return []byte(strings.Repeat(shape.nest, n) + shape.leaf + strings.Repeat(shape.unnest, n))
			}

			// At the ceiling both accept, so the preflight adds no depth
			// strictness of its own.
			atCeiling := body(stdlibMaxDepth)
			var sink any
			if err := json.Unmarshal(atCeiling, &sink); err != nil {
				t.Fatalf("json.Unmarshal(%d nested) = %v, want it accepted; the stdlib ceiling moved and stdlibMaxDepth = %d is stale",
					stdlibMaxDepth, err, stdlibMaxDepth)
			}
			if err := jsonx.Preflight(bytes.NewReader(atCeiling)); err != nil {
				t.Errorf("Preflight(%d nested) = %v, want it accepted (json.Unmarshal accepts it)", stdlibMaxDepth, err)
			}

			// One level over, both reject, and Preflight surfaces the
			// tokenizer's own error rather than translating it.
			overDeep := body(stdlibMaxDepth + 1)
			if err := json.Unmarshal(overDeep, &sink); err == nil {
				t.Fatalf("json.Unmarshal(%d nested) = nil, want it rejected; the stdlib ceiling moved and stdlibMaxDepth = %d is stale",
					stdlibMaxDepth+1, stdlibMaxDepth)
			}
			err := jsonx.Preflight(bytes.NewReader(overDeep))
			if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
				t.Errorf("Preflight(%d nested) = %v (%T), want encoding/json's *json.SyntaxError", stdlibMaxDepth+1, err, err)
			}
		})
	}
}

// TestPreflightBoundsAllOpensBody pins the hostile shape the package was
// built for: 1 MiB of nothing but '['. It must be refused by the nesting
// ceiling after stdlibMaxDepth frames rather than by recursing once per byte to
// discover the body is truncated, so the cost is bounded by the ceiling and
// not by the input length.
func TestPreflightBoundsAllOpensBody(t *testing.T) {
	t.Parallel()
	if err := jsonx.Preflight(strings.NewReader(strings.Repeat("[", 1<<20))); err == nil {
		t.Error("Preflight(1 MiB of open brackets) = nil, want it rejected")
	}
}

// TestPreflightAcceptsKeyDenseObject pins the O(keys) cost of the
// fold-canonicalized set: a wide object of distinct keys is accepted, which an
// O(keys^2) EqualFold scan would also do - but only after quadratic work on an
// upstream-controlled key count.
func TestPreflightAcceptsKeyDenseObject(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteByte('{')
	for i := range 20000 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"k`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`":1`)
	}
	b.WriteByte('}')
	if err := jsonx.Preflight(strings.NewReader(b.String())); err != nil {
		t.Errorf("Preflight(key-dense object) = %v, want it accepted", err)
	}
}

// TestPreflightRejectsTrailingData pins that the preflight is never looser
// than the decode step it precedes: whole-input strictness, like End.
func TestPreflightRejectsTrailingData(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{} {}`, `{"name":"a"} x`, `null 1`, `1 2`} {
		if err := jsonx.Preflight(strings.NewReader(body)); err == nil {
			t.Errorf("Preflight(%q) = nil error, want trailing data rejected", body)
		}
	}
}

// TestPreflightBoundsKeySnippet pins that an oversized untrusted key cannot
// balloon the error string, and that the rendered message stays single-line
// even when the key carries control bytes.
func TestPreflightBoundsKeySnippet(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("k", 5000)
	err := jsonx.Preflight(strings.NewReader(`{"` + long + `":1,"` + long + `":2}`))
	if !errors.Is(err, jsonx.ErrDuplicateKey) {
		t.Fatalf("Preflight(oversized duplicate key) = %v, want ErrDuplicateKey", err)
	}
	if len(err.Error()) > 200 {
		t.Errorf("error string is %d bytes, want the key snippet bounded: %q", len(err.Error()), err.Error())
	}

	withControl := `{"a\nb":1,"a\nb":2}`
	err = jsonx.Preflight(strings.NewReader(withControl))
	if !errors.Is(err, jsonx.ErrDuplicateKey) {
		t.Fatalf("Preflight(%q) = %v, want ErrDuplicateKey", withControl, err)
	}
	if strings.ContainsAny(err.Error(), "\n\r") {
		t.Errorf("error string carries a raw newline: %q", err.Error())
	}
}

// TestMapEntryCapRejects pins the per-map cardinality cap: it trips on the
// entry that would exceed it (not one early, not one late), names the map via
// what, and an at-cap map decodes cleanly.
func TestMapEntryCapRejects(t *testing.T) {
	t.Parallel()
	body := []byte(`{"meta":{"a":"1","b":"2","c":"3","d":"4"}}`)
	_, err := budgetWidget(body, 3, 0, 0)
	if !errors.Is(err, jsonx.ErrMapCap) {
		t.Fatalf("err = %v, want ErrMapCap", err)
	}
	if !strings.Contains(err.Error(), "meta") {
		t.Errorf("err = %q, want the map named via what", err)
	}
	got, err := budgetWidget(body, 4, 0, 0)
	if err != nil {
		t.Fatalf("at-cap decode = %v, want nil", err)
	}
	if len(got.Meta) != 4 {
		t.Errorf("Meta = %v, want all 4 entries at the cap", got.Meta)
	}
	// The cap counts THIS map's entries, not the body's: two maps of three
	// entries each pass a cap of three.
	if _, err := budgetWidget([]byte(`{"meta":{"a":"1","b":"2","c":"3"},"nested":{"x":{"p":"1"}}}`), 3, 0, 0); err != nil {
		t.Errorf("two under-cap maps = %v, want nil", err)
	}
}

// TestMapEntriesChargeTheAggregateBudget pins that map entries are charged
// against the Decoder's aggregate budget - the thing a caller hand-walking a
// map cannot do - and that Elements reports them.
func TestMapEntriesChargeTheAggregateBudget(t *testing.T) {
	t.Parallel()
	body := []byte(`{"meta":{"a":"1","b":"2","c":"3"}}`)
	if _, err := budgetWidget(body, 0, 0, 2); !errors.Is(err, jsonx.ErrElementBudget) {
		t.Fatalf("budget 2: err = %v, want ErrElementBudget", err)
	}
	if _, err := budgetWidget(body, 0, 0, 3); err != nil {
		t.Errorf("budget 3: err = %v, want nil", err)
	}
	d := jsonx.NewDecoder(bytes.NewReader(body), 0)
	var w widget
	if err := decodeWidget(d, &w, 0, 0); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := d.Elements(); got != 3 {
		t.Errorf("Elements() = %d, want 3 map entries charged", got)
	}
}

// TestArrayAndMapEntriesShareOneAggregate is the reason Map charges through the
// same counter Array does: a body's array elements and map entries must draw on
// ONE budget, so a caller cannot be amplified by splitting hostile cardinality
// across container kinds the way two independent budgets allow.
func TestArrayAndMapEntriesShareOneAggregate(t *testing.T) {
	t.Parallel()
	// 2 tags + 2 parts + 3 meta entries + 2 nested entries (outer) + 2 inner
	// = 11 charges, whichever container they belong to.
	body := []byte(`{"tags":["a","b"],"parts":[{"id":1},{"id":2}],` +
		`"meta":{"a":"1","b":"2","c":"3"},"nested":{"o":{"x":"1"},"p":{"y":"2"}}}`)
	d := jsonx.NewDecoder(bytes.NewReader(body), 0)
	var w widget
	if err := decodeWidget(d, &w, 0, 0); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := d.Elements(); got != 11 {
		t.Fatalf("Elements() = %d, want 11 (2 tags + 2 parts + 3 meta + 2 outer + 2 inner)", got)
	}
	if _, err := budgetWidget(body, 0, 0, 10); !errors.Is(err, jsonx.ErrElementBudget) {
		t.Errorf("budget 10: err = %v, want ErrElementBudget (the two kinds share one aggregate)", err)
	}
	if _, err := budgetWidget(body, 0, 0, 11); err != nil {
		t.Errorf("budget 11: err = %v, want nil", err)
	}
	// The proof that the aggregate is shared and not per-kind: a budget that
	// covers the arrays alone is not enough once the maps charge too.
	arraysOnly := []byte(`{"tags":["a","b"],"parts":[{"id":1},{"id":2}]}`)
	if _, err := budgetWidget(arraysOnly, 0, 0, 4); err != nil {
		t.Errorf("arrays alone under budget 4 = %v, want nil", err)
	}
	if _, err := budgetWidget([]byte(`{"tags":["a","b"],"parts":[{"id":1},{"id":2}],"meta":{"a":"1"}}`), 0, 0, 4); !errors.Is(err, jsonx.ErrElementBudget) {
		t.Errorf("one map entry past the array budget = %v, want ErrElementBudget", err)
	}
}

// TestMapCapCheckedBeforeBudgetAndKey pins the check order the twin exists to
// own: the per-map cap fires before the aggregate charge, and both fire before
// the entry's key - itself an unbounded allocation from the wire - is read.
func TestMapCapCheckedBeforeBudgetAndKey(t *testing.T) {
	t.Parallel()
	// The 4th entry crosses the per-map cap of 3; the budget (also 3) would
	// have tripped on the same entry, so an ErrMapCap here proves the cap ran
	// first.
	body := []byte(`{"meta":{"a":"1","b":"2","c":"3","d":"4"}}`)
	if _, err := budgetWidget(body, 3, 0, 3); !errors.Is(err, jsonx.ErrMapCap) {
		t.Fatalf("err = %v, want the per-map cap to fire before the budget charge", err)
	}
	// With no cap, the budget stops the walk BEFORE the over-budget entry's
	// key is read, so the map holds exactly the charged entries.
	d := jsonx.NewDecoder(strings.NewReader(`{"a":"1","b":"2","c":"3"}`), 2)
	m, err := d.Map(nil, 0, "meta", func(_ string, v *string) error { return d.Decode(v) })
	if !errors.Is(err, jsonx.ErrElementBudget) {
		t.Fatalf("err = %v, want ErrElementBudget", err)
	}
	if len(m) != 2 {
		t.Errorf("map holds %d entries (%v), want the 2 that were charged", len(m), m)
	}
	if got := d.Elements(); got != 3 {
		t.Errorf("Elements() = %d, want 3 (the over-budget entry is charged, then refused)", got)
	}
}

// TestMapNestedSharesCapsAndBudget pins that a map nested in a map is bounded
// like any other container: the inner walk carries its own cap and charges the
// same aggregate, so a body cannot hide cardinality one level down.
func TestMapNestedSharesCapsAndBudget(t *testing.T) {
	t.Parallel()
	body := []byte(`{"nested":{"o":{"a":"1","b":"2"},"p":{"c":"3"}}}`)
	got, err := budgetWidget(body, 0, 0, 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]map[string]string{"o": {"a": "1", "b": "2"}, "p": {"c": "3"}}
	if !reflect.DeepEqual(got.Nested, want) {
		t.Errorf("Nested = %v, want %v", got.Nested, want)
	}
	// A cap of 2 admits the outer map (2 entries) and the larger inner map (2
	// entries); a cap of 1 is tripped by the inner one, which is the level a
	// per-container cap on the outer walk alone would have missed.
	if _, err := budgetWidget(body, 2, 0, 0); err != nil {
		t.Errorf("cap 2 = %v, want nil", err)
	}
	if _, err := budgetWidget(body, 1, 0, 0); !errors.Is(err, jsonx.ErrMapCap) {
		t.Errorf("cap 1 = %v, want ErrMapCap from a nested map", err)
	}
	// 2 outer entries + 3 inner entries all charge the one aggregate.
	if _, err := budgetWidget(body, 0, 0, 4); !errors.Is(err, jsonx.ErrElementBudget) {
		t.Errorf("budget 4 = %v, want ErrElementBudget (5 entries across two levels)", err)
	}
	if _, err := budgetWidget(body, 0, 0, 5); err != nil {
		t.Errorf("budget 5 = %v, want nil", err)
	}
}

// TestMapNullYieldsNilLikeUnmarshal pins the null handling that is easy to get
// backwards: for a MAP a null yields nil (wiping an earlier occurrence), which
// follows Unmarshal's null-into-slice rather than its null-into-struct no-op.
// The stdlib cross-check is the point - a change in either direction is a
// parity break, not a preference.
func TestMapNullYieldsNilLikeUnmarshal(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{"meta":null}`, `{"meta":{"a":"1"},"meta":null}`} {
		got, err := budgetWidget([]byte(body), 0, 0, 0)
		if err != nil {
			t.Fatalf("bounded decode %q: %v", body, err)
		}
		if got.Meta != nil {
			t.Errorf("bounded(%q) Meta = %v, want nil", body, got.Meta)
		}
		var want widget
		if err := json.Unmarshal([]byte(body), &want); err != nil {
			t.Fatalf("json.Unmarshal %q: %v", body, err)
		}
		if want.Meta != nil {
			t.Errorf("json.Unmarshal(%q) Meta = %v, want nil; the parity premise is stale", body, want.Meta)
		}
	}
	// A wrong-shaped value is an error and leaves the caller's own map alone,
	// exactly as Unmarshal leaves a target it could not decode into.
	prior := map[string]string{"pre": "x"}
	d := jsonx.NewDecoder(strings.NewReader(`[]`), 0)
	m, err := d.Map(prior, 0, "meta", func(_ string, v *string) error { return d.Decode(v) })
	if err == nil {
		t.Fatal("Map over an array = nil error, want a shape error")
	}
	if !reflect.DeepEqual(m, prior) {
		t.Errorf("Map returned %v on a shape error, want the caller's prior map %v", m, prior)
	}
}
