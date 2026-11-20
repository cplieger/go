package httpwire

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/cplieger/go/httpserver/v2"
)

// CheckAnswer is one source's answer to an open check, a keyed re-read of
// one item at a source it came from.
type CheckAnswer string

// The open check's answers.
const (
	CheckSame    CheckAnswer = "same"    // found, and the stored row already matches it
	CheckUpdated CheckAnswer = "updated" // found, and the item changed
	CheckGone    CheckAnswer = "gone"    // the source no longer has the item
	CheckError   CheckAnswer = "error"   // the read failed; Code says why
)

// NotStored is why an updated or gone answer was not stored.
type NotStored string

// The reasons an answer was not stored.
const (
	NotStoredSuperseded NotStored = "superseded"       // the stored row moved while the check read
	NotStoredUnwritable NotStored = "store_unwritable" // the store refused the write
)

// Checked is an open check's answer: one entry per source the item came
// from, each source named once.
type Checked[T any] struct {
	Sources []SourceCheck[T] `json:"sources"`
}

// SourceCheck is one source's answer. Which fields are set follows Answer:
//   - same: none of Row, Stored, NotStored, Code and Upstream;
//   - updated: Row (as stored when Stored, else as the source answered it),
//     and exactly one of Stored and NotStored;
//   - gone: exactly one of Stored and NotStored, and no Row;
//   - error: Code, and Upstream when the source answered; no Row, Stored
//     or NotStored.
type SourceCheck[T any] struct {
	Row      *T        `json:"row,omitempty"`
	Upstream *Upstream `json:"upstream,omitempty"`
	// Source is the app's name for the source, unique in its Checked.
	Source    string      `json:"source"`
	Answer    CheckAnswer `json:"answer"`
	NotStored NotStored   `json:"not_stored,omitempty"`
	// Code is the failure's code, in the code grammar.
	Code httpserver.ErrorCode `json:"code,omitempty"`
	// Position is the change feed's Feed-Position value right after the
	// write, set exactly when Stored, so a tab marks seen no further than
	// the write it read.
	Position string `json:"position,omitempty"`
	Stored   bool   `json:"stored"`
}

// checkAnswer is implemented by every Checked[T], and by a type embedding
// one, which checkedIndex tells apart.
type checkAnswer interface {
	checkedType() reflect.Type
	bound(dropped func(source, service string)) (out any, problem string)
}

func (Checked[T]) checkedType() reflect.Type { return reflect.TypeFor[Checked[T]]() }

// bound answers c as it is written, its nil Sources as [] and each upstream
// held to the envelope's rules (dropped is told of one left out), or the
// first rule c breaks.
func (c Checked[T]) bound(dropped func(source, service string)) (out any, problem string) {
	b := Checked[T]{Sources: make([]SourceCheck[T], len(c.Sources))}
	seen := make(map[string]bool, len(c.Sources))
	for i, s := range c.Sources {
		if problem = s.problem(); problem != "" {
			return nil, fmt.Sprintf("source %d (%q): %s", i, s.Source, problem)
		}
		if seen[s.Source] {
			return nil, fmt.Sprintf("source %q is named twice", s.Source)
		}
		seen[s.Source] = true
		u, ok := boundUpstream(s.Upstream)
		if !ok {
			dropped(s.Source, s.Upstream.Service)
		}
		s.Upstream = u
		b.Sources[i] = s
	}
	return b, ""
}

// problem names the first rule of SourceCheck's doc s breaks, or "".
func (s *SourceCheck[T]) problem() string {
	switch {
	case s.Source == "":
		return "source is empty"
	case s.NotStored != "" && s.NotStored != NotStoredSuperseded && s.NotStored != NotStoredUnwritable:
		return fmt.Sprintf("not_stored %q is not superseded or store_unwritable", s.NotStored)
	case s.Answer == CheckError:
		return s.errorProblem()
	case !slices.Contains([]CheckAnswer{CheckSame, CheckUpdated, CheckGone}, s.Answer):
		return fmt.Sprintf("answer %q is not same, updated, gone or error", s.Answer)
	case s.Code != "" || s.Upstream != nil:
		return fmt.Sprintf("a %s answer carries a code or an upstream, which only error does", s.Answer)
	case s.Answer == CheckSame:
		return s.sameProblem()
	}
	return s.storedProblem()
}

func (s *SourceCheck[T]) sameProblem() string {
	switch {
	case s.Stored || s.NotStored != "":
		return "a same answer is stored or carries not_stored"
	case s.Row != nil:
		return "a same answer carries a row"
	}
	return ""
}

// storedProblem holds an updated or gone answer to its row and to exactly
// one of stored and not_stored.
func (s *SourceCheck[T]) storedProblem() string {
	switch {
	case s.Answer == CheckUpdated && s.Row == nil:
		return "an updated answer carries no row"
	case s.Answer == CheckGone && s.Row != nil:
		return "a gone answer carries a row"
	case s.Stored == (s.NotStored != ""):
		return fmt.Sprintf("a %s answer is not exactly one of stored and not_stored", s.Answer)
	}
	return ""
}

func (s *SourceCheck[T]) errorProblem() string {
	switch {
	case !isCode(string(s.Code)) || len(s.Code) > maxEnvelopeString:
		return "an error answer's code is not in the code grammar"
	case s.Row != nil || s.Stored || s.NotStored != "":
		return "an error answer carries a row, stored or not_stored"
	}
	return ""
}

// checkedIndex answers the index of the Checked[T] that t is or embeds,
// empty when t is one.
func checkedIndex(t reflect.Type) ([]int, bool) {
	return answerIndex(t, func(t reflect.Type) bool {
		c, ok := methodOf[checkAnswer](t)
		return ok && c.checkedType() == t
	})
}

// boundChecked answers body with the Checked[T] it is or embeds bound, or
// the rule that Checked breaks; a body holding none is answered as it is.
func boundChecked(body any, dropped func(source, service string)) (out any, problem string) {
	v := reflect.ValueOf(body)
	if !v.IsValid() {
		return body, ""
	}
	idx, ok := checkedIndex(v.Type())
	if !ok {
		return body, ""
	}
	o := reflect.New(v.Type()).Elem()
	o.Set(v)
	f := o.FieldByIndex(idx)
	c, _ := reflect.TypeAssert[checkAnswer](f)
	b, problem := c.bound(dropped)
	if problem != "" {
		return nil, problem
	}
	f.Set(reflect.ValueOf(b))
	return o.Interface(), ""
}
