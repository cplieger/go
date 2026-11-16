package chartdata

import (
	"errors"
	"testing"

	"github.com/cplieger/go/measure/v2"
)

func TestDeclarationOf(t *testing.T) {
	t.Parallel()
	var nilGauge *measure.Gauge
	for _, tc := range []struct {
		decl measure.Declared
		name string
	}{
		{name: "nil", decl: nil},
		{name: "typed nil", decl: nilGauge},
		{name: "no name", decl: measure.NewGauge("", "help", measure.UnitNone)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := declarationOf(tc.decl, "decl")
			var refusal *RefusalError
			if !errors.As(err, &refusal) || refusal.Rule != RuleDeclaration || refusal.Field != "decl" {
				t.Errorf("declarationOf = %v, want a %q refusal of decl", err, RuleDeclaration)
			}
		})
	}
	t.Run("named", func(t *testing.T) {
		t.Parallel()
		decl, err := declarationOf(measure.NewGauge("queue_depth", "Jobs waiting.", measure.UnitCount), "decl")
		if err != nil {
			t.Fatalf("declarationOf: %v", err)
		}
		if decl.Name != "queue_depth" || decl.Unit != measure.UnitCount {
			t.Errorf("declaration = %q in %q, want queue_depth in count", decl.Name, decl.Unit)
		}
	})
}
