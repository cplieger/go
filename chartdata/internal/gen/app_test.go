package gen_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/cplieger/go/chartdata"
	"github.com/cplieger/go/chartdata/internal/gen"
	"github.com/cplieger/go/chartdata/internal/gen/app"
	"github.com/cplieger/go/wiregen/v2"
)

// interfaces maps each exported interface in a generated types file to its
// declaration.
func interfaces(t *testing.T, file string) map[string]string {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	out := map[string]string{}
	for _, m := range regexp.MustCompile(`(?ms)^export interface (\w+) \{\n.*?^\}`).FindAllStringSubmatch(string(src), -1) {
		out[m[1]] = m[0]
	}
	return out
}

// TestAnAppsGeneratorRegistersChartdatasTypes generates an app's wire the way
// an app's own wiregen run does, registering chartdata's types from the
// dependency package beside its own, and checks that each chartdata type
// comes out as the contract's frame.ts declares it, so a frame the app's
// decoders read passes to the ui elements unchanged.
func TestAnAppsGeneratorRegistersChartdatasTypes(t *testing.T) {
	r := wiregen.Registry{
		Types:  []wiregen.Type{wiregen.TypeRef[app.Dashboard](), wiregen.TypeRef[app.Event]()},
		Events: []wiregen.Event{{Name: "frame", Data: wiregen.TypeRef[chartdata.FrameEvent]()}},
	}
	for _, typ := range chartdata.WireTypes() {
		r.Types = append(r.Types, wiregen.TypeOf(typ))
	}
	tsDir := filepath.Join(t.TempDir(), "wire")
	if err := r.Generate(t.Context(), wiregen.WithTSDir(tsDir)); err != nil {
		t.Fatalf("an app's Generate: %v", err)
	}
	got := interfaces(t, filepath.Join(tsDir, "types.gen.ts"))
	for _, name := range []string{"Dashboard", "Event"} {
		if _, ok := got[name]; !ok {
			t.Errorf("the app's types file has no %s", name)
		}
	}
	contract := interfaces(t, filepath.Join(root, "contract", gen.TypesFile))
	if len(contract) != len(chartdata.WireTypes()) {
		t.Errorf("frame.ts declares %d interfaces, want one per chartdata wire type, %d", len(contract), len(chartdata.WireTypes()))
	}
	for name, want := range contract {
		if got[name] != want {
			t.Errorf("the app's %s =\n%s\nwant frame.ts's\n%s", name, got[name], want)
		}
	}
	if _, err := os.Stat(filepath.Join(tsDir, "events.gen.ts")); err != nil {
		t.Errorf("the app's events module: %v, want a decoder for the frame event", err)
	}
}
