package chartdata_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/chartdata"
)

// TestWireTypesListEveryTypeOnTheWire walks the fields of every frame and of
// FrameEvent: each struct they reach is listed, so an app registering the
// list registers the whole wire, and each name keeps clear of an app's own.
func TestWireTypesListEveryTypeOnTheWire(t *testing.T) {
	t.Parallel()
	listed := chartdata.WireTypes()
	reached := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || reached[typ] {
			return
		}
		reached[typ] = true
		for f := range typ.Fields() {
			walk(f.Type)
		}
	}
	walk(reflect.TypeFor[chartdata.FrameEvent]())
	for typ := range reached {
		if !slices.Contains(listed, typ) {
			t.Errorf("%s is on the wire but not in WireTypes", typ.Name())
		}
	}
	for _, typ := range listed {
		if !reached[typ] {
			t.Errorf("%s is in WireTypes but on no frame or event", typ.Name())
		}
		if name := typ.Name(); !strings.HasSuffix(name, "Frame") && !strings.HasPrefix(name, "Frame") {
			t.Errorf("%s neither ends nor starts with Frame, so it can meet an app's own type", name)
		}
	}
	if got := len(listed); got != 26 {
		t.Errorf("WireTypes lists %d types, want 26: 12 frames, FrameEvent and 13 parts", got)
	}
	listed[0] = nil
	if chartdata.WireTypes()[0] == nil {
		t.Error("WireTypes shares its slice between calls")
	}
}

// TestWireEnumsListEveryEnumOnTheWire walks the same fields: each string
// type they reach is listed, so the generated frame type, axis and event
// kind name their members instead of any string.
func TestWireEnumsListEveryEnumOnTheWire(t *testing.T) {
	t.Parallel()
	listed := chartdata.WireEnums()
	reached := map[reflect.Type]bool{}
	for _, typ := range chartdata.WireTypes() {
		for f := range typ.Fields() {
			ft := f.Type
			for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.String && ft != reflect.TypeFor[string]() {
				reached[ft] = true
			}
		}
	}
	for typ := range reached {
		if !slices.Contains(listed, typ) {
			t.Errorf("%s is on the wire but not in WireEnums", typ.Name())
		}
	}
	for _, typ := range listed {
		if !reached[typ] {
			t.Errorf("%s is in WireEnums but on no frame or event", typ.Name())
		}
	}
	listed[0] = nil
	if chartdata.WireEnums()[0] == nil {
		t.Error("WireEnums shares its slice between calls")
	}
}
