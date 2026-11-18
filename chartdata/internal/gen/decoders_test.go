package gen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/chartdata"
	"github.com/cplieger/go/wiregen/v2"
)

// decodeFixtures runs every frame and count fixture, and the read-state
// refusal's frame, through the decoders an app's wiregen run writes for
// chartdata's types, and prints per fixture the type of the as_of its read
// carries, "-" for a frame with no read, or "refused".
const decodeFixtures = `import { readdirSync, readFileSync } from "node:fs";
import * as decoders from "./decoders.gen.ts";
const dir = process.argv[2];
const decode = (name: string, body: any): any => {
  const type = body.type ?? "";
  const decoder = name.startsWith("count-") ? "decodeCount" : "decode" + type.charAt(0).toUpperCase() + type.slice(1) + "Frame";
  return (decoders as any)[decoder](body);
};
for (const name of readdirSync(dir).sort()) {
  let body = JSON.parse(readFileSync(dir + "/" + name, "utf8"));
  if (name === "refusal-read-state.json") {
    body = body.frame;
  } else if (!name.startsWith("frame-") && !name.startsWith("count-")) {
    continue;
  }
  try {
    const v = decode(name, body);
    const read = name.startsWith("count-") ? v : v.read;
    console.log(name + " " + (read === undefined ? "-" : read.as_of === null ? "null" : typeof read.as_of));
  } catch {
    console.log(name + " refused");
  }
}
`

// TestDecodersReadEveryFixture holds the fixtures to the TypeScript an app
// decodes them with: each frame and count decodes, a read's as_of reaches
// the page as a string, or null before the first whole read, and a state
// outside the six is refused.
func TestDecodersReadEveryFixture(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node is not on PATH: %v", err)
	}
	var r wiregen.Registry
	for _, typ := range chartdata.WireTypes() {
		r.Types = append(r.Types, wiregen.TypeOf(typ))
	}
	for _, typ := range chartdata.WireEnums() {
		r.Enums = append(r.Enums, wiregen.EnumRef{Type: wiregen.TypeOf(typ)})
	}
	dir := t.TempDir()
	if err := r.Generate(t.Context(), wiregen.WithTSDir(dir),
		wiregen.WithValidatorsImport("./validators.gen.ts"), wiregen.WithTypesImport("./types.gen.ts")); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for name, body := range map[string]string{"package.json": `{"type": "module"}`, "main.ts": decodeFixtures} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fixtures, err := filepath.Abs(filepath.Join(root, "contract", "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	// Node strips the types of a .ts module natively from 22.6
	// (https://nodejs.org/api/typescript.html#type-stripping).
	cmd := exec.CommandContext(t.Context(), node, "--experimental-strip-types", "--no-warnings", "main.ts", fixtures)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	want := []string{
		"count-blind.json string", "count-complete.json string", "count-excluded.json null",
		"count-partial.json string", "count-unread.json null", "count-unsupported.json null",
	}
	entries, err := os.ReadDir(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch name := e.Name(); {
		case name == "frame-series-read.json":
			want = append(want, name+" string")
		case strings.HasPrefix(name, "frame-"):
			want = append(want, name+" -")
		}
	}
	want = append(want, "refusal-read-state.json refused")
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the decoders read the fixtures as\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
