package chartdata

import (
	"reflect"

	"github.com/cplieger/go/measure/v2"
)

// WireTypes returns every struct type this package puts on the wire, the
// twelve frames, their parts, FrameEvent, Read and Count, for an app's code
// generator to register beside WireEnums, such as with wiregen.TypeOf. A
// frame's name ends in Frame and a part's starts with Frame, so an app's own
// types rarely meet them, but an app type named Read or Count needs another
// name: wiregen refuses a clash by name. Each call returns a fresh slice.
func WireTypes() []reflect.Type {
	return []reflect.Type{
		reflect.TypeFor[SeriesFrame](),
		reflect.TypeFor[BinsFrame](),
		reflect.TypeFor[CategoriesFrame](),
		reflect.TypeFor[BoxesFrame](),
		reflect.TypeFor[PointsFrame](),
		reflect.TypeFor[GridFrame](),
		reflect.TypeFor[CalendarFrame](),
		reflect.TypeFor[TreeFrame](),
		reflect.TypeFor[FlowFrame](),
		reflect.TypeFor[EventsFrame](),
		reflect.TypeFor[StatesFrame](),
		reflect.TypeFor[ValueFrame](),
		reflect.TypeFor[FrameEvent](),
		reflect.TypeFor[FrameRef](),
		reflect.TypeFor[FrameNote](),
		reflect.TypeFor[FrameSeries](),
		reflect.TypeFor[FrameBoxes](),
		reflect.TypeFor[FrameUnits](),
		reflect.TypeFor[FramePoints](),
		reflect.TypeFor[FrameGridAxis](),
		reflect.TypeFor[FrameTreePart](),
		reflect.TypeFor[FrameTreeGroup](),
		reflect.TypeFor[FrameFlowNode](),
		reflect.TypeFor[FrameFlowLink](),
		reflect.TypeFor[FrameTimes](),
		reflect.TypeFor[FrameMeter](),
		reflect.TypeFor[Read](),
		reflect.TypeFor[Count](),
	}
}

// WireEnums returns the string enums on the wire, FrameType, FrameAxis,
// FrameEventKind and measure.ReadState, for an app's code generator to
// register beside WireTypes, such as with wiregen.EnumRef{Type:
// wiregen.TypeOf(t)}, so the frame's type, an axis, an event's kind and a
// read's state are typed by their members. ReadState's bare name is taken
// too, so an app type or enum named ReadState needs another name: wiregen
// refuses a clash by name. Each call returns a fresh slice.
func WireEnums() []reflect.Type {
	return []reflect.Type{
		reflect.TypeFor[FrameType](),
		reflect.TypeFor[FrameAxis](),
		reflect.TypeFor[FrameEventKind](),
		reflect.TypeFor[measure.ReadState](),
	}
}

// WireUnion is a wire struct tagged by one of its fields: each of Cases'
// keys is a value of the field Tag names, listing the wire names of the
// other fields that value allows.
type WireUnion struct {
	Type  reflect.Type
	Cases map[string][]string
	Tag   string
}

// WireUnions returns the tagged wire structs, FrameEvent by its kind, for an
// app's code generator to register beside WireTypes, such as with
// wiregen.Union, so each kind of event is typed by its own fields. Each call
// returns fresh values.
func WireUnions() []WireUnion {
	keyed := func(fields ...string) []string { return append([]string{"key"}, fields...) }
	return []WireUnion{{
		Type: reflect.TypeFor[FrameEvent](),
		Tag:  "kind",
		Cases: map[string][]string{
			string(KindAppend):  keyed("x", "x1", "x2", "values"),
			string(KindReplace): keyed("x1", "x2", "open", "values"),
			string(KindReset):   keyed("at"),
			string(KindSet): keyed("series", "bins", "categories", "boxes", "points", "grid",
				"calendar", "tree", "flow", "events", "states", "value"),
		},
	}}
}
