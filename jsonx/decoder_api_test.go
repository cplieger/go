package jsonx_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/go/jsonx"
)

// decodeInts walks one array of numbers through d, uncapped per array.
func decodeInts(d *jsonx.Decoder) ([]int, error) {
	return d.Array(nil, 0, "ints", func(v *int) error { return d.Decode(v) })
}

// TestDecoderNext_CarriesTheBudget pins that a budget carried with Next spans
// every body: the charge continues where the previous Decoder stopped, and a
// spent budget fails the next body's first element rather than reading as no
// budget at all.
func TestDecoderNext_CarriesTheBudget(t *testing.T) {
	t.Parallel()
	first := jsonx.NewDecoder(strings.NewReader(`[1,2]`), 3)
	if _, err := decodeInts(first); err != nil {
		t.Fatalf("first page: %v", err)
	}
	second := first.Next(strings.NewReader(`[3,4]`))
	if got := second.Elements(); got != 2 {
		t.Errorf("Next(...).Elements() = %d, want the 2 already charged", got)
	}
	if _, err := decodeInts(second); !errors.Is(err, jsonx.ErrElementBudget) {
		t.Errorf("second page past a budget of 3 = %v, want ErrElementBudget", err)
	}
	if got := first.Elements(); got != 2 {
		t.Errorf("first.Elements() after Next = %d, want 2: Next must not change d", got)
	}

	spent := jsonx.NewDecoder(strings.NewReader(`[1,2,3]`), 3)
	if _, err := decodeInts(spent); err != nil {
		t.Fatalf("exact-budget page: %v", err)
	}
	if _, err := decodeInts(spent.Next(strings.NewReader(`[4]`))); !errors.Is(err, jsonx.ErrElementBudget) {
		t.Errorf("first element after a spent budget = %v, want ErrElementBudget", err)
	}
	if got, err := decodeInts(spent.Next(strings.NewReader(`[]`))); err != nil || len(got) != 0 {
		t.Errorf("empty page after a spent budget = (%v, %v), want ([], nil)", got, err)
	}

	unbounded := jsonx.NewDecoder(strings.NewReader(`[1,2]`), 0)
	if _, err := decodeInts(unbounded); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeInts(unbounded.Next(strings.NewReader(`[3,4,5]`))); err != nil {
		t.Errorf("Next of an unbudgeted Decoder = %v, want no budget there either", err)
	}
}

// TestDecoderArray_KeepsDecodedElementsOnError pins Array's error result, the
// same rule Map follows: the caller's slice when the value is not an array,
// and the elements decoded so far after a cap, budget or element failure.
func TestDecoderArray_KeepsDecodedElementsOnError(t *testing.T) {
	t.Parallel()
	prior := []int{7, 8, 9}
	for _, tc := range []struct {
		name   string
		body   string
		cap    int
		budget int
		want   []int
		is     error
	}{
		{"not an array keeps prior", `{"a":1}`, 0, 0, []int{7, 8, 9}, nil},
		{"array cap keeps the decoded head", `[1,2,3]`, 2, 0, []int{1, 2}, jsonx.ErrArrayCap},
		{"budget keeps the decoded head", `[1,2,3]`, 0, 1, []int{1}, jsonx.ErrElementBudget},
		{"element failure keeps the elements before it", `[1,"x",3]`, 0, 0, []int{1}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := append([]int(nil), prior...)
			d := jsonx.NewDecoder(strings.NewReader(tc.body), tc.budget)
			got, err := d.Array(in, tc.cap, "ints", func(v *int) error { return d.Decode(v) })
			if err == nil {
				t.Fatalf("Array(%s) = (%v, nil), want an error", tc.body, got)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("Array(%s) error = %v, want %v", tc.body, err, tc.is)
			}
			if !equalInts(got, tc.want) {
				t.Errorf("Array(%s) on error returned %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDecoderShapeErrors_QuoteNoDocumentBytes pins that Open and Key name the
// unexpected token by kind: a wire string can be any length and carry raw
// controls, and none of it may reach the error text a caller logs.
func TestDecoderShapeErrors_QuoteNoDocumentBytes(t *testing.T) {
	t.Parallel()
	hostile := strings.Repeat("A", 4096) + `\n\u001b[2J`
	for _, tc := range []struct {
		name string
		body string
		call func(*jsonx.Decoder) error
		want string
	}{
		{"Open on a string", `"` + hostile + `"`, func(d *jsonx.Decoder) error { _, err := d.Open('{'); return err }, `jsonx: expected '{', got a string`},
		{"Open on a number", `12345678901234567890`, func(d *jsonx.Decoder) error { _, err := d.Open('['); return err }, `jsonx: expected '[', got a number`},
		{"Open on a boolean", `true`, func(d *jsonx.Decoder) error { _, err := d.Open('{'); return err }, `jsonx: expected '{', got a boolean`},
		{"Open on the wrong delimiter", `[]`, func(d *jsonx.Decoder) error { _, err := d.Open('{'); return err }, `jsonx: expected '{', got '['`},
		{"Key on a number in an array", `[42]`, func(d *jsonx.Decoder) error {
			if _, err := d.Open('['); err != nil {
				return err
			}
			_, err := d.Key()
			return err
		}, `jsonx: expected an object key, got a number`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.call(jsonx.NewDecoder(strings.NewReader(tc.body), 0))
			if err == nil || err.Error() != tc.want {
				t.Errorf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
