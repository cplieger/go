package textsafe_test

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cplieger/go/textsafe"
)

// ExampleSanitize sanitizes an upstream-controlled title before it becomes
// a slog attribute: the terminal-escape introducer and the bidi override
// become spaces, ordinary text passes through.
func ExampleSanitize() {
	title := "Frieren\x1b[2J \u202egpj.exe"
	fmt.Printf("%q\n", textsafe.Sanitize(title))
	// Output: "Frieren [2J  gpj.exe"
}

// ExampleSanitizeSingleLine flattens an upstream error message for a
// one-line sink: the newline that would forge a second log record becomes a
// space along with the escape introducer.
func ExampleSanitizeSingleLine() {
	msg := "bad request\nlevel=ERROR forged\x1b[2J"
	fmt.Printf("%q\n", textsafe.SanitizeSingleLine(msg))
	// Output: "bad request level=ERROR forged [2J"
}

// ExampleCapBytes bounds a sanitized string without splitting a multi-byte
// rune: the cut backs off to a rune boundary instead of leaving a partial
// rune's raw tail bytes.
func ExampleCapBytes() {
	s := "葬送のフリーレン" // three bytes per rune
	fmt.Printf("%q\n", textsafe.CapBytes(s, 7))
	// Output: "葬送"
}

// ExampleCapBytesTail keeps the END of a sanitized string, for a value whose
// identifying part sits at its tail. The marker goes in FRONT and is the
// caller's own composition, exactly as SanitizeSingleLineBounded appends one
// behind the head cap.
func ExampleCapBytesTail() {
	s := "葬送のフリーレン" // three bytes per rune
	fmt.Printf("%q\n", textsafe.CapBytesTail(s, 7))
	fmt.Printf("%q\n", "..."+textsafe.CapBytesTail(s, 7))
	// Output:
	// "レン"
	// "...レン"
}

// ExampleRedactSingleLineCapped bounds an upstream error body that echoed the
// request's API key and tried to forge a second log record.
func ExampleRedactSingleLineCapped() {
	body := "invalid key sk-123456\nlevel=ERROR forged"
	text, cut := textsafe.RedactSingleLineCapped(body, 32, "...", "sk-123456")
	fmt.Printf("%q %v\n", text, cut)
	// Output: "invalid key [redacted] level=..." true
}

// ExampleIsUnsafeMultiLine shows the CR/LF policy split: a newline is safe
// for a sink whose encoder escapes it, and unsafe for a single-line sink.
func ExampleIsUnsafeMultiLine() {
	fmt.Println(textsafe.IsUnsafeMultiLine('\n'), textsafe.IsUnsafeSingleLine('\n'))
	fmt.Println(textsafe.IsUnsafeMultiLine('\u009b'), textsafe.IsUnsafeSingleLine('\u009b'))
	// Output:
	// false true
	// true true
}

// ExampleSanitizeSingleLineBounded bounds an upstream error message for a
// capped log attribute: sanitization happens before the cap is measured, a
// within-cap result comes back untouched, and an over-cap result is cut on
// a rune boundary with the "..." marker appended outside the cap (at most
// n+3 bytes).
func ExampleSanitizeSingleLineBounded() {
	fmt.Printf("%q\n", textsafe.SanitizeSingleLineBounded("bad\nrequest", 20))
	fmt.Printf("%q\n", textsafe.SanitizeSingleLineBounded("葬送のフリーレン", 7))
	// Output:
	// "bad request"
	// "葬送..."
}

// ExampleIsBidiControl classifies a right-to-left override against a plain
// letter.
func ExampleIsBidiControl() {
	fmt.Println(textsafe.IsBidiControl('\u202e'), textsafe.IsBidiControl('a'))
	// Output: true false
}

// ExampleIsUnsafeNonASCII classifies the runes a URL percent-escaper must
// encode even though url.Parse accepts them raw: the C1 escape introducer
// and the bidi override are flagged, while an ASCII control (the sink's own
// escaping covers it) and a plain letter are not.
func ExampleIsUnsafeNonASCII() {
	fmt.Println(textsafe.IsUnsafeNonASCII('\u009b'), textsafe.IsUnsafeNonASCII('\u202e'))
	fmt.Println(textsafe.IsUnsafeNonASCII('\x1b'), textsafe.IsUnsafeNonASCII('é'))
	// Output:
	// true true
	// false false
}

// ExampleUntrusted tags an upstream field at the decode boundary: the raw
// bytes survive ingestion for matching (Raw), while fmt, errors, and JSON
// emission all render the sanitized form automatically.
func ExampleUntrusted() {
	var ep struct {
		Title textsafe.Untrusted `json:"title"`
	}
	_ = json.Unmarshal([]byte(`{"title":"Frieren\u202egpj.exe"}`), &ep)

	fmt.Println(len(ep.Title.Raw()))             // raw bytes preserved for compute
	fmt.Println(ep.Title)                        // fmt renders sanitized
	fmt.Println(fmt.Errorf("bad: %s", ep.Title)) // errors carry sanitized text
	out, _ := json.Marshal(ep)
	fmt.Println(string(out)) // encoders emit sanitized
	// Output:
	// 17
	// Frieren gpj.exe
	// bad: Frieren gpj.exe
	// {"title":"Frieren gpj.exe"}
}

// ExampleUntrusted_mapKey keys a JSON document by untrusted text: the map
// looks keys up by their raw bytes, and the encoder emits each key sanitized.
func ExampleUntrusted_mapKey() {
	title := textsafe.NewUntrusted("Frieren\u202egpj.exe")
	counts := map[textsafe.Untrusted]int{title: 3}

	fmt.Println(counts[textsafe.NewUntrusted("Frieren\u202egpj.exe")]) // raw lookup
	out, _ := json.Marshal(counts)
	fmt.Println(string(out))
	// Output:
	// 3
	// {"Frieren gpj.exe":3}
}

// ExampleSanitizeCapped bounds a multi-line upstream body for a JSON sink:
// CR/LF survive the sanitizer, the caller's own marker is charged against
// the cap so the result never exceeds it, and cut carries the truncation
// fact for the caller to record.
func ExampleSanitizeCapped() {
	body, cut := textsafe.SanitizeCapped("line one\nline two\x1b[2J", 16, "...(truncated)")
	fmt.Printf("%q %v %d\n", body, cut, len(body))

	body, cut = textsafe.SanitizeCapped("short\nbody", 16, "...(truncated)")
	fmt.Printf("%q %v\n", body, cut)
	// Output:
	// "li...(truncated)" true 16
	// "short\nbody" false
}

// ExampleSanitizeSingleLineCapped bounds an upstream error for a capped log
// attribute and logs the truncation as a fact rather than inferring it from
// the marker.
func ExampleSanitizeSingleLineCapped() {
	reason, cut := textsafe.SanitizeSingleLineCapped("bad request\nlevel=ERROR forged", 14, "...")
	fmt.Printf("%q %v %d\n", reason, cut, len(reason))
	// Output: "bad request..." true 14
}

// ExampleSanitizeBudgeted bounds an upstream value whose size the caller does
// not control. The value is three megabytes of right-to-left overrides behind
// one letter, and the budget is eight bytes: the pre-cap form cuts the RAW
// input at the budget and sanitizes only that, while SanitizeCapped sanitizes
// the whole value first and then fills the same budget from the megabyte of
// spaces that produces.
func ExampleSanitizeBudgeted() {
	huge := "A" + strings.Repeat("\u202e", 1<<20)

	text, cut := textsafe.SanitizeBudgeted(huge, 8, "...")
	fmt.Printf("%q %v\n", text, cut)

	text, cut = textsafe.SanitizeCapped(huge, 8, "...")
	fmt.Printf("%q %v\n", text, cut)
	// Output:
	// "A  ..." true
	// "A    ..." true
}

// ExampleBudget spends one shared byte budget across several untrusted values
// and keeps ONE truncation fact for the whole aggregate: each value is capped
// before it is sanitized, the separators are charged too, and the marker is
// charged inside the budget, so the joined attribute lands exactly on it with a
// single marker rather than one per value.
func ExampleBudget() {
	b := textsafe.NewBudget(24, "...")
	for i, group := range []string{"SubsPlease", "Erai-raws\u202e", "LostYears", "PMR"} {
		if i > 0 && !b.Write(", ") {
			break
		}
		if !b.Write(group) {
			break
		}
	}
	attr, cut := b.Result()
	fmt.Printf("%q %v %d\n", attr, cut, len(attr))
	// Output: "SubsPlease, Erai-raws..." true 24
}
