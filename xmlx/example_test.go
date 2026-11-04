package xmlx_test

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strings"

	"github.com/cplieger/go/xmlx"
)

// The gate runs over the raw bytes before the decoder sees them, so a document
// outside the caller's contract costs one scan instead of a decode.
func ExamplePreflight() {
	body := []byte(`<rss><channel><title>Example</title></channel></rss>`)

	if err := xmlx.Preflight(body, xmlx.DefaultLimits()); err != nil {
		fmt.Println("rejected:", err)
		return
	}
	fmt.Println("accepted")
	// Output: accepted
}

// A rejection names which bound fired, so a consumer can log the specific
// contract the upstream broke instead of "too big".
func ExamplePreflight_limitError() {
	// A body nested far past a feed's real shape: each 3 bytes of wire would
	// cost the decoder one live open-element stack entry.
	body := []byte(strings.Repeat("<a>", 5000))

	err := xmlx.Preflight(body, xmlx.DefaultLimits())

	if le, ok := errors.AsType[*xmlx.LimitError](err); ok {
		fmt.Println(le.Kind, "limit:", le.Limit)
	}
	fmt.Println("is a limit error:", errors.Is(err, xmlx.ErrLimit))
	// Output:
	// element nesting depth limit: 64
	// is a limit error: true
}

// An XML directive is refused as a class: bounding one truthfully means
// reproducing encoding/xml's own directive tokenizer, so the package declines
// instead of reporting a bound it cannot honor.
func ExamplePreflight_directive() {
	err := xmlx.Preflight([]byte(`<!DOCTYPE rss><rss/>`), xmlx.DefaultLimits())
	fmt.Println(err)
	// Output: xmlx: XML directives are not allowed
}

// A schema decoder charges each retained value against one Budget, so the
// document is bounded by what it makes the program HOLD, not only by its wire
// size.
func ExampleBudget() {
	const doc = `<item><title>Show &amp; Tale</title><guid>x1</guid></item>`

	budget := mustBudget(64, 4096)
	var title, guid string

	d := xml.NewDecoder(strings.NewReader(doc))
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "title":
			title, err = budget.DecodeText(d)
		case "guid":
			guid, err = budget.DecodeText(d)
		}
		if err != nil {
			fmt.Println("rejected:", err)
			return
		}
	}

	fmt.Printf("%s / %s / charged %d bytes\n", title, guid, budget.Total())
	// Output: Show & Tale / x1 / charged 13 bytes
}

// The per-field cap applies while the value accumulates, so a value split across
// CDATA seams, every chunk individually small, is refused at the token that
// would cross it rather than after the whole string exists.
func ExampleBudget_DecodeText_splitValue() {
	doc := "<title>" + strings.Repeat("<![CDATA[xxxxxxxx]]>", 20) + "</title>"

	budget := mustBudget(32, 4096)
	d := xml.NewDecoder(strings.NewReader(doc))
	if _, err := d.Token(); err != nil {
		return
	}

	_, err := budget.DecodeText(d)
	fmt.Println(err)
	fmt.Println("charged:", budget.Total())
	// Output:
	// xmlx: decoded field longer than 32 bytes
	// charged: 0
}
