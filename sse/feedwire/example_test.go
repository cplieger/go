package feedwire_test

import (
	"errors"
	"fmt"

	"github.com/cplieger/go/sse/v2/feedwire"
)

func ExampleEncodeChanged() {
	data, err := feedwire.EncodeChanged(feedwire.Changed{
		Source: "store",
		Epoch:  "9f2c4e1a7b3d5f60819a2b3c4d5e6f70",
		Rev:    1234,
		Kinds:  []string{"budgets", "runs"},
	})
	fmt.Println(string(data), err)
	// Output:
	// {"source":"store","epoch":"9f2c4e1a7b3d5f60819a2b3c4d5e6f70","rev":1234,"kinds":["budgets","runs"]} <nil>
}

func ExampleEncodeListening() {
	data, err := feedwire.EncodeListening(feedwire.Listening{
		Wire: "3f9a0c1d2e4b5a67",
		Sources: []feedwire.ListeningSource{{
			Source: "store",
			Epoch:  "9f2c4e1a7b3d5f60819a2b3c4d5e6f70",
			Floors: map[string]uint64{"runs": 1230, "budgets": 1101},
		}},
	})
	fmt.Println(string(data), err)
	// Output:
	// {"feed":1,"wire":"3f9a0c1d2e4b5a67","sources":[{"source":"store","epoch":"9f2c4e1a7b3d5f60819a2b3c4d5e6f70","floors":{"budgets":1101,"runs":1230}}]} <nil>
}

func ExampleDecodeListening_otherFormat() {
	data := []byte(`{"feed":2,"wire":"3f9a0c1d2e4b5a67","views":[]}`)
	_, err := feedwire.DecodeListening(data)
	frozen, _ := feedwire.PeekListening(data)
	fmt.Println(errors.Is(err, feedwire.ErrOtherFormat), frozen.Feed, frozen.Wire)
	// Output:
	// true 2 3f9a0c1d2e4b5a67
}

func ExampleFormatPosition() {
	v, err := feedwire.FormatPosition([]feedwire.Position{
		{Source: "store", Epoch: "9f2c4e1a7b3d5f60819a2b3c4d5e6f70", Rev: 1234},
		{Source: "live", Epoch: "41ab0c9d8e7f6a5b4c3d2e1f00112233", Rev: 7},
	})
	fmt.Println(v, err)
	ps, err := feedwire.ParsePosition(v)
	fmt.Println(ps[1].Source, ps[1].Rev, err)
	// Output:
	// store=1234;epoch="9f2c4e1a7b3d5f60819a2b3c4d5e6f70", live=7;epoch="41ab0c9d8e7f6a5b4c3d2e1f00112233" <nil>
	// live 7 <nil>
}
