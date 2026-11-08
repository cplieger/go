package xmlx_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/go/xmlx"
)

// TestLimitErrorMessages pins the rendered text of every bound. These strings are
// what an operator reads in a log line when an upstream starts sending documents
// the gate refuses, so each one has to name the bound and its configured value -
// "limit exceeded" alone gives nobody a next step.
func TestLimitErrorMessages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind  xmlx.Kind
		limit int
		want  string
	}{
		{kind: xmlx.KindTextRun, limit: 64, want: "xmlx: text run longer than 64 bytes"},
		{kind: xmlx.KindCDATA, limit: 64, want: "xmlx: CDATA section longer than 64 bytes"},
		{kind: xmlx.KindComment, limit: 128, want: "xmlx: comment longer than 128 bytes"},
		{kind: xmlx.KindProcInst, limit: 128, want: "xmlx: processing instruction longer than 128 bytes"},
		{kind: xmlx.KindToken, limit: 128, want: "xmlx: markup token longer than 128 bytes"},
		{kind: xmlx.KindTagAttrs, limit: 16, want: "xmlx: attributes on one start tag over 16"},
		{kind: xmlx.KindDepth, limit: 64, want: "xmlx: element nesting depth over 64"},
		{kind: xmlx.KindElements, limit: 100_000, want: "xmlx: element count over 100000"},
		{kind: xmlx.KindDirective, limit: 0, want: "xmlx: XML directives are not allowed"},
		{kind: xmlx.KindField, limit: 4096, want: "xmlx: decoded field longer than 4096 bytes"},
		{kind: xmlx.KindTotalText, limit: 1024, want: "xmlx: cumulative decoded text longer than 1024 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.kind.String(), func(t *testing.T) {
			t.Parallel()
			err := &xmlx.LimitError{Kind: tt.kind, Limit: tt.limit}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
			if !errors.Is(err, xmlx.ErrLimit) {
				t.Error("does not match ErrLimit")
			}
			if errors.Is(err, xmlx.ErrInvalidLimits) {
				t.Error("matches ErrInvalidLimits: a document rejection would read as a caller mistake")
			}
		})
	}
}

// TestLimitErrorCarriesNoDocumentBytes pins the deliberate absence: the offending
// bytes are untrusted input, and putting an excerpt in the error would put an
// unbounded unsanitized string on its way to a log line: the amplification this
// package exists to stop, reintroduced through its own diagnostics.
func TestLimitErrorCarriesNoDocumentBytes(t *testing.T) {
	t.Parallel()
	const marker = "SECRETMARKER"
	doc := "<a>" + strings.Repeat(marker, 500) + "</a>"
	err := xmlx.Preflight([]byte(doc), xmlx.Limits{
		MaxTextRunBytes: 8, MaxTokenBytes: 64, MaxTagAttrs: 4, MaxDepth: 4, MaxElements: 1 << 20,
	})
	if err == nil {
		t.Fatal("Preflight accepted an over-long text run")
	}
	if strings.Contains(err.Error(), marker) {
		t.Errorf("error text quotes the document: %q", err.Error())
	}
	if len(err.Error()) > 120 {
		t.Errorf("error text is %d bytes, want a bounded message: %q", len(err.Error()), err.Error())
	}
}

// TestKindStringNamesEveryBound pins that every Kind renders a distinct,
// non-placeholder noun, so a consumer logging Kind gets something readable rather
// than an integer or a shared fallback.
func TestKindStringNamesEveryBound(t *testing.T) {
	t.Parallel()
	kinds := []xmlx.Kind{
		xmlx.KindTextRun, xmlx.KindCDATA, xmlx.KindComment, xmlx.KindProcInst,
		xmlx.KindToken, xmlx.KindTagAttrs, xmlx.KindDepth, xmlx.KindElements, xmlx.KindDirective,
		xmlx.KindField, xmlx.KindTotalText,
	}
	seen := make(map[string]xmlx.Kind, len(kinds))
	for _, k := range kinds {
		got := k.String()
		if got == "" || got == xmlx.KindUnknown.String() {
			t.Errorf("Kind(%d).String() = %q, want a specific noun", k, got)
		}
		if prior, dup := seen[got]; dup {
			t.Errorf("Kind(%d) and Kind(%d) both render %q", prior, k, got)
		}
		seen[got] = k
	}
	if got := xmlx.KindUnknown.String(); got != "unknown bound" {
		t.Errorf("KindUnknown.String() = %q, want %q", got, "unknown bound")
	}
	// A value outside the enum must not render as a real bound.
	if got := xmlx.Kind(200).String(); got != "unknown bound" {
		t.Errorf("Kind(200).String() = %q, want the unknown fallback", got)
	}
}

// TestConfigErrorNamesTheField pins the configuration-mistake message. It is read
// by a developer wiring the library up, so it must say which of several bounds is
// unset and what they set it to.
func TestConfigErrorNamesTheField(t *testing.T) {
	t.Parallel()
	err := &xmlx.ConfigError{Field: "Limits.MaxDepth", Value: -1}
	if got, want := err.Error(), "xmlx: Limits.MaxDepth must not be negative, got -1"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, xmlx.ErrInvalidLimits) {
		t.Error("does not match ErrInvalidLimits")
	}
	if errors.Is(err, xmlx.ErrLimit) {
		t.Error("matches ErrLimit: a caller mistake would read as a document rejection")
	}
	if got := fmt.Sprintf("%v", &xmlx.ConfigError{Field: "BudgetLimits.MaxTotalBytes", Value: -3}); !strings.Contains(got, "-3") {
		t.Errorf("Error() = %q, want the offending value included", got)
	}
}
