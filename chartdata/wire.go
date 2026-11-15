package chartdata

import "reflect"

// WireTypes returns every struct type this package puts on the wire, the
// twelve frames, their parts and FrameEvent, for an app's code generator to
// register, such as with wiregen.TypeOf. Each name is unique to this
// package's wire: a frame's ends in Frame, and every other one starts with
// Frame, so none collides with an app's own type. Each call returns a fresh
// slice.
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
