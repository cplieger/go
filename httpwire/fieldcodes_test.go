package httpwire_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/cplieger/go/httpwire"
)

// The TS half's test compares SCHEMA_FIELD_CODES with the same file, so a code
// added on one side fails both in one change.
const fieldCodesFile = "web/testdata/field-codes.json"

func TestFieldCodesFile(t *testing.T) {
	b, err := os.ReadFile(fieldCodesFile)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, httpwire.FieldCodes) {
		t.Fatalf("%s lists %q, want the field-code map's %q", fieldCodesFile, got, httpwire.FieldCodes)
	}
}
