package chartdata

import (
	"reflect"
	"time"
)

// FrameEventKind says how a FrameEvent changes a frame already drawn.
type FrameEventKind string

// The kinds of FrameEvent.
const (
	// KindAppend adds one column to a SeriesFrame, or one bin to a BinsFrame.
	KindAppend FrameEventKind = "append"
	// KindReplace changes a frame's last column or open bin in place.
	KindReplace FrameEventKind = "replace"
	// KindReset marks a restart: a line breaks there instead of joining
	// across it, and a state history shows a gap.
	KindReset FrameEventKind = "reset"
	// KindSet replaces the whole frame.
	KindSet FrameEventKind = "set"
)

// FrameEvent changes a frame already drawn, sent on the app's own event
// stream. Key names the frame the page drew; each kind sets only its own
// fields, and a set event exactly one frame field, the one its frame's type
// names. Build one with AppendColumn, AppendBin, ReplaceColumn, ReplaceBin,
// ResetAt or SetFrame.
type FrameEvent struct {
	X  *float64 `json:"x,omitempty"`
	X1 *float64 `json:"x1,omitempty"`
	X2 *float64 `json:"x2,omitempty"`
	// Start, on an append, is the window's new start when it slides by more
	// than the column dropped; columns before it drop.
	Start *float64 `json:"start,omitempty"`
	Open  *bool    `json:"open,omitempty"`
	// At is a reset's instant, in Unix milliseconds.
	At         *int64           `json:"at,omitempty"`
	Series     *SeriesFrame     `json:"series,omitempty"`
	Bins       *BinsFrame       `json:"bins,omitempty"`
	Categories *CategoriesFrame `json:"categories,omitempty"`
	Boxes      *BoxesFrame      `json:"boxes,omitempty"`
	Points     *PointsFrame     `json:"points,omitempty"`
	Grid       *GridFrame       `json:"grid,omitempty"`
	Calendar   *CalendarFrame   `json:"calendar,omitempty"`
	Tree       *TreeFrame       `json:"tree,omitempty"`
	Flow       *FlowFrame       `json:"flow,omitempty"`
	Events     *EventsFrame     `json:"events,omitempty"`
	States     *StatesFrame     `json:"states,omitempty"`
	Value      *ValueFrame      `json:"value,omitempty"`
	Key        string           `json:"key"`
	Kind       FrameEventKind   `json:"kind"`
	// Values are the new column's or bin's values in the frame's series
	// order, null for a gap.
	Values []*float64 `json:"values,omitempty"`
}

// Frame is a pointer to one of this package's frame types, which SetFrame
// carries.
type Frame interface {
	setInto(e *FrameEvent)
}

func (f *SeriesFrame) setInto(e *FrameEvent) {
	c := *f
	e.Series = &c
}

func (f *BinsFrame) setInto(e *FrameEvent) {
	c := *f
	e.Bins = &c
}

func (f *CategoriesFrame) setInto(e *FrameEvent) {
	c := *f
	e.Categories = &c
}

func (f *BoxesFrame) setInto(e *FrameEvent) {
	c := *f
	e.Boxes = &c
}

func (f *PointsFrame) setInto(e *FrameEvent) {
	c := *f
	e.Points = &c
}

func (f *GridFrame) setInto(e *FrameEvent) {
	c := *f
	e.Grid = &c
}

func (f *CalendarFrame) setInto(e *FrameEvent) {
	c := *f
	e.Calendar = &c
}

func (f *TreeFrame) setInto(e *FrameEvent) {
	c := *f
	e.Tree = &c
}

func (f *FlowFrame) setInto(e *FrameEvent) {
	c := *f
	e.Flow = &c
}

func (f *EventsFrame) setInto(e *FrameEvent) {
	c := *f
	e.Events = &c
}

func (f *StatesFrame) setInto(e *FrameEvent) {
	c := *f
	e.States = &c
}

func (f *ValueFrame) setInto(e *FrameEvent) {
	c := *f
	e.Value = &c
}

// AppendColumn adds the column at x, with a value per series of the
// SeriesFrame key names, NaN for a gap. x must be finite and after the
// frame's last x, which the page checks.
func AppendColumn(key string, x float64, values []float64) (FrameEvent, error) {
	if err := checkFinite("x", x); err != nil {
		return FrameEvent{}, err
	}
	vs, err := gapped("values", values)
	if err != nil {
		return FrameEvent{}, err
	}
	return FrameEvent{Key: key, Kind: KindAppend, X: &x, Values: vs}, nil
}

// AppendBin adds bin, with a value per series of the BinsFrame key names,
// NaN for a gap. The bin's From must be below its To, both finite.
func AppendBin(key string, bin Bin, values []float64) (FrameEvent, error) {
	return binEvent(key, KindAppend, bin, values, nil)
}

// ReplaceColumn sets the values of the last column of the SeriesFrame key
// names, NaN for a gap, and says whether it is still filling.
func ReplaceColumn(key string, values []float64, open bool) (FrameEvent, error) {
	vs, err := gapped("values", values)
	if err != nil {
		return FrameEvent{}, err
	}
	return FrameEvent{Key: key, Kind: KindReplace, Values: vs, Open: &open}, nil
}

// ReplaceBin sets the last bin of the BinsFrame key names to bin, whose end
// may have moved, with its values, and says whether it is still filling.
func ReplaceBin(key string, bin Bin, values []float64, open bool) (FrameEvent, error) {
	return binEvent(key, KindReplace, bin, values, &open)
}

func binEvent(key string, kind FrameEventKind, bin Bin, values []float64, open *bool) (FrameEvent, error) {
	if err := checkFinite("bin.from", bin.From); err != nil {
		return FrameEvent{}, err
	}
	if err := checkFinite("bin.to", bin.To); err != nil {
		return FrameEvent{}, err
	}
	if bin.From >= bin.To {
		return FrameEvent{}, refuse(RuleBin, "bin", "must end above where it starts")
	}
	vs, err := gapped("values", values)
	if err != nil {
		return FrameEvent{}, err
	}
	return FrameEvent{Key: key, Kind: kind, X1: &bin.From, X2: &bin.To, Values: vs, Open: open}, nil
}

// ResetAt marks a restart of the frame key names at at.
func ResetAt(key string, at time.Time) FrameEvent {
	ms := at.UnixMilli()
	return FrameEvent{Key: key, Kind: KindReset, At: &ms}
}

// SetFrame replaces the frame key names with a copy of f, built by this
// package's constructors. It refuses a nil f.
func SetFrame(key string, f Frame) (FrameEvent, error) {
	if f == nil || reflect.ValueOf(f).IsNil() {
		return FrameEvent{}, refuse(RuleEmpty, "frame", "is nil")
	}
	e := FrameEvent{Key: key, Kind: KindSet}
	f.setInto(&e)
	return e, nil
}
