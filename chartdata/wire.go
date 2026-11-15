package chartdata

import "reflect"

// WireTypes returns every struct type this package puts on the wire, the
// twelve frames, their parts and FrameEvent, for an app's code generator to
// register beside WireEnums, such as with wiregen.TypeOf. A frame's name
// ends in Frame and every other one starts with Frame, so an app's own types
// rarely meet them; wiregen refuses a clash by name. Each call returns a
// fresh slice.
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
	}
}

// WireEnums returns the string enums on the wire, FrameType, FrameAxis and
// FrameEventKind, for an app's code generator to register beside WireTypes,
// such as with wiregen.EnumRef{Type: wiregen.TypeOf(t)}, so the frame's type
// and an event's kind are typed by their members. Each call returns a fresh
// slice.
func WireEnums() []reflect.Type {
	return []reflect.Type{
		reflect.TypeFor[FrameType](),
		reflect.TypeFor[FrameAxis](),
		reflect.TypeFor[FrameEventKind](),
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
