package chartdata

import (
	"math"
	"slices"

	"github.com/cplieger/go/measure/v2"
)

// Flow is what NewFlowFrame draws: its links, and optionally its nodes'
// order and, for an alluvial diagram, its stages.
type Flow struct {
	// Nodes, when set, orders the nodes; every link names only listed nodes
	// and every listed node is named by a link.
	Nodes []string
	// Stages, when set, makes an alluvial diagram of two or more stages.
	Stages []string
	Links  []FlowLink
}

// FlowLink is one link given to NewFlowFrame: an amount, 0 or more or NaN
// for no data, from one node to another. In an alluvial diagram Stage names
// the stage the link leaves, which may be left empty with two stages.
type FlowLink struct {
	From  string
	To    string
	Stage string
	Value float64
}

// NewFlowFrame builds a FlowFrame of d's amounts. Names are never empty, no
// two links join the same nodes at the same stage, and without stages the
// links form no loop, since a node in a loop has no column. Each stage a
// link names is listed and not the last, and with three stages or more every
// link names one. It takes WithRefs and WithNotes.
func NewFlowFrame(d measure.Declared, f Flow, opts ...Option) (FlowFrame, error) {
	decl, o, err := begin(d, opts, optMarks, len(f.Links)+len(f.Nodes), func() error { return checkFlowNames(f) })
	if err != nil {
		return FlowFrame{}, err
	}
	links, err := buildLinks(f.Links, f.Stages)
	if err != nil {
		return FlowFrame{}, err
	}
	var nodes []FrameFlowNode
	if f.Nodes != nil {
		nodes = make([]FrameFlowNode, len(f.Nodes))
		for i, n := range f.Nodes {
			nodes[i] = FrameFlowNode{Key: n}
		}
	}
	return FlowFrame{
		Type: TypeFlow, Name: decl.Name, Unit: string(decl.Unit),
		Nodes: nodes, Stages: slices.Clone(f.Stages), Links: links,
		Refs: o.refs, Notes: o.notes, Read: o.frameRead,
	}, nil
}

func buildLinks(in []FlowLink, stages []string) ([]FrameFlowLink, error) {
	links := make([]FrameFlowLink, len(in))
	seen := make(map[[3]string]struct{}, len(in))
	for i, l := range in {
		field := indexed("links", i)
		stage, err := linkStage(field, l, stages)
		if err != nil {
			return nil, err
		}
		join := [3]string{l.From, l.To, stage}
		if _, dup := seen[join]; dup {
			return nil, refusef(RuleDuplicate, field, "joins %q to %q again", l.From, l.To)
		}
		seen[join] = struct{}{}
		if err := checkPart(field+".value", l.Value); err != nil {
			return nil, err
		}
		links[i] = FrameFlowLink{From: l.From, To: l.To, Stage: l.Stage}
		if value := l.Value; !math.IsNaN(value) {
			links[i].Value = &value
		}
	}
	if stages == nil {
		return links, checkAcyclic(in)
	}
	return links, nil
}

func checkFlowNames(f Flow) error {
	if f.Stages != nil {
		if len(f.Stages) < 2 {
			return refuse(RuleShape, "stages", "must hold two or more, joined stage to stage")
		}
		if err := checkNames("stages", f.Stages); err != nil {
			return err
		}
	}
	for i, l := range f.Links {
		if l.From == "" || l.To == "" {
			return refuse(RuleEmpty, indexed("links", i), "must name both of its nodes")
		}
	}
	if f.Nodes == nil {
		return nil
	}
	return checkFlowNodes(f.Nodes, f.Links)
}

func checkFlowNodes(nodes []string, links []FlowLink) error {
	if err := checkNames("nodes", nodes); err != nil {
		return err
	}
	named := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		named[n] = false
	}
	for i, l := range links {
		for _, end := range [2]string{l.From, l.To} {
			if _, ok := named[end]; !ok {
				return refusef(RuleShape, indexed("links", i), "names %q, which nodes does not list", end)
			}
			named[end] = true
		}
	}
	for i, n := range nodes {
		if !named[n] {
			return refusef(RuleShape, indexed("nodes", i), "lists %q, which no link names", n)
		}
	}
	return nil
}

// linkStage returns the stage l leaves, the first with two stages and none
// named, or "" in a Sankey diagram.
func linkStage(field string, l FlowLink, stages []string) (string, error) {
	switch {
	case stages == nil && l.Stage != "":
		return "", refuse(RuleShape, field+".stage", "has no place without stages")
	case stages == nil:
		if l.From == l.To {
			return "", refuse(RuleShape, field, "joins a node to itself, a loop with no column")
		}
		return "", nil
	case l.Stage == "" && len(stages) == 2:
		return stages[0], nil
	case l.Stage == "":
		return "", refuse(RuleEmpty, field+".stage", "must name the stage it leaves, with three stages or more")
	}
	i := slices.Index(stages, l.Stage)
	switch {
	case i < 0:
		return "", refusef(RuleShape, field+".stage", "names %q, which stages does not list", l.Stage)
	case i == len(stages)-1:
		return "", refuse(RuleShape, field+".stage", "names the last stage, which has no next")
	}
	return l.Stage, nil
}

// checkAcyclic refuses links that lead from a node back to it: Kahn's
// topological sort leaves the nodes on or after a loop unsorted.
func checkAcyclic(links []FlowLink) error {
	next := make(map[string][]string)
	incoming := make(map[string]int)
	for _, l := range links {
		next[l.From] = append(next[l.From], l.To)
		incoming[l.To]++
		if _, ok := incoming[l.From]; !ok {
			incoming[l.From] = 0
		}
	}
	var ready []string
	for n, k := range incoming {
		if k == 0 {
			ready = append(ready, n)
		}
	}
	sorted := 0
	for len(ready) > 0 {
		n := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		sorted++
		for _, m := range next[n] {
			incoming[m]--
			if incoming[m] == 0 {
				ready = append(ready, m)
			}
		}
	}
	if sorted < len(incoming) {
		return refuse(RuleShape, "links", "lead from a node back to it, a loop with no column")
	}
	return nil
}
