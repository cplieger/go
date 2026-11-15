package chartdata

import "fmt"

// Rule names the rule a refused frame breaks. The rules are stable: the
// refusal fixtures carry them, so the chart elements can refuse the same
// frames for the same reason.
type Rule string

// The rules a constructor refuses a frame by.
const (
	// RuleDeclaration is a nil declaration, or one with no name.
	RuleDeclaration Rule = "declaration"
	// RuleOption is an option the frame cannot take, or a bad option value.
	RuleOption Rule = "option"
	// RuleLength is two fields that must be as long as each other and are not.
	RuleLength Rule = "length"
	// RuleOrder is an x, an instant or a bin edge out of order, or repeated
	// where it must strictly increase.
	RuleOrder Rule = "order"
	// RuleBin is a bin whose x1 is not below its x2.
	RuleBin Rule = "bin"
	// RuleFinite is an infinite value, which JSON cannot carry, or a NaN
	// where no gap is allowed.
	RuleFinite Rule = "finite"
	// RuleMagnitude is a value beyond ±[MaxMagnitude].
	RuleMagnitude Rule = "magnitude"
	// RuleNegative is a negative value where it is a part of a whole.
	RuleNegative Rule = "negative"
	// RuleDuplicate is a key or name given twice where each must be unique.
	RuleDuplicate Rule = "duplicate"
	// RuleEmpty is an empty name, key or list where one is required.
	RuleEmpty Rule = "empty"
	// RuleShape is a frame whose parts do not fit together, such as a box
	// with some of its five numbers and not the others.
	RuleShape Rule = "shape"
	// RuleLimit is a frame past one of the limits in [MaxValues] and beside it.
	RuleLimit Rule = "limit"
)

// RefusalError is the error a constructor returns for a frame it refuses.
type RefusalError struct {
	// Rule is the rule the frame breaks.
	Rule Rule
	// Field is the path of the field at fault, such as "series[2].values".
	Field string
	// Reason says what is wrong with it.
	Reason string
}

func (e *RefusalError) Error() string {
	return fmt.Sprintf("chartdata: %s %s (%s)", e.Field, e.Reason, e.Rule)
}

func refuse(rule Rule, field, reason string) *RefusalError {
	return &RefusalError{Rule: rule, Field: field, Reason: reason}
}

func refusef(rule Rule, field, format string, args ...any) *RefusalError {
	return refuse(rule, field, fmt.Sprintf(format, args...))
}
