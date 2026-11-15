package chartdata

import (
	"math"

	"github.com/cplieger/go/measure/v2"
)

// TreePart is a part given to a tree constructor: its key and its value, 0
// or more, or NaN for no data.
type TreePart struct {
	Key   string
	Value float64
}

// TreeGroup is a group given to NewGroupedTreeFrame: its key and its parts,
// one or more.
type TreeGroup struct {
	Key      string
	Children []TreePart
}

// NewTreeFrame builds a one-level TreeFrame of d's parts, in the order to
// lay them out. Keys are unique and never empty. It takes WithRefs and
// WithNotes.
func NewTreeFrame(d measure.Declared, parts []TreePart, opts ...Option) (TreeFrame, error) {
	decl, o, err := begin(d, opts, optMarks, len(parts))
	if err != nil {
		return TreeFrame{}, err
	}
	out, err := buildParts("parts", parts)
	if err != nil {
		return TreeFrame{}, err
	}
	return TreeFrame{
		Type: TypeTree, Name: decl.Name, Unit: string(decl.Unit),
		Parts: out, Refs: o.refs, Notes: o.notes,
	}, nil
}

// NewGroupedTreeFrame builds a two-level TreeFrame of d's groups and their
// parts, in the order to lay them out. Each group holds a part at least, and
// siblings' keys are unique and never empty. It takes WithRefs and WithNotes.
func NewGroupedTreeFrame(d measure.Declared, groups []TreeGroup, opts ...Option) (TreeFrame, error) {
	charge := len(groups)
	for _, g := range groups {
		charge += len(g.Children)
	}
	decl, o, err := begin(d, opts, optMarks, charge, func() error {
		keys := make([]string, len(groups))
		for i, g := range groups {
			keys[i] = g.Key
		}
		return checkNames("groups", keys)
	})
	if err != nil {
		return TreeFrame{}, err
	}
	out := make([]FrameTreeGroup, len(groups))
	for i, g := range groups {
		field := indexed("groups", i) + ".children"
		if len(g.Children) == 0 {
			return TreeFrame{}, refuse(RuleEmpty, field, "must hold a part")
		}
		children, err := buildParts(field, g.Children)
		if err != nil {
			return TreeFrame{}, err
		}
		out[i] = FrameTreeGroup{Key: g.Key, Children: children}
	}
	return TreeFrame{
		Type: TypeTree, Name: decl.Name, Unit: string(decl.Unit),
		Groups: out, Refs: o.refs, Notes: o.notes,
	}, nil
}

func buildParts(field string, parts []TreePart) ([]FrameTreePart, error) {
	keys := make([]string, len(parts))
	vs := make([]float64, len(parts))
	for i, p := range parts {
		keys[i], vs[i] = p.Key, p.Value
		if err := checkPart(indexed(field, i)+".value", p.Value); err != nil {
			return nil, err
		}
	}
	if err := checkNames(field, keys); err != nil {
		return nil, err
	}
	values, err := gapped(field, vs)
	if err != nil {
		return nil, err
	}
	out := make([]FrameTreePart, len(parts))
	for i, p := range parts {
		out[i] = FrameTreePart{Key: p.Key, Value: values[i]}
	}
	return out, nil
}

// checkPart refuses a part's value unless it is 0 or more, or NaN for no
// data, as a tree part's or a flow link's is.
func checkPart(field string, v float64) error {
	if math.IsNaN(v) {
		return nil
	}
	if err := checkFinite(field, v); err != nil {
		return err
	}
	if v < 0 {
		return refuse(RuleNegative, field, "must be 0 or more, a part of the whole")
	}
	return nil
}
