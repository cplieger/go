//go:build integration

package wiregen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/go/wiregen/v2"
	"github.com/cplieger/go/wiregen/v2/testdata/edges"
	"github.com/cplieger/go/wiregen/v2/testdata/maps"
)

// The named map decoders and the client that sends and reads them
// type-check under tsc, and under node decode each value with its decoder,
// read null as empty and refuse a non-object or a bad value.
func TestMapType_DecoderRuns(t *testing.T) {
	out := runTS(t, mapReg(), map[string]string{
		"api-client.ts": transportTS(t),
		"main.ts": `import { decodeLabels, decodeSettings } from "./decoders.gen.js";
import type { Labels, Settings } from "./types.gen.js";
const labels: Labels = { a: { name: "x" } };
const settings: Settings = { theme: "dark", nested: { any: [1] } };
void labels;
void settings;
for (const body of ['{"a":{"name":"x"}}', 'null', '{}', '[]', '{"a":{"nom":"x"}}', '{"a":null}']) {
  try {
    console.log("ok " + JSON.stringify(decodeLabels(JSON.parse(body))));
  } catch (e) {
    console.log("refused " + (e as Error).message);
  }
}
console.log("ok " + JSON.stringify(decodeSettings(JSON.parse('{"theme":"dark","n":[1]}'))));
`,
	})
	want := strings.Join([]string{
		`ok {"a":{"name":"x"}}`,
		`ok {}`,
		`ok {}`,
		`refused $.labels: expected object, got array`,
		`refused $.labels.a: $.item.name: expected string, got undefined`,
		`refused $.labels.a: $.item: expected object, got null`,
		`ok {"theme":"dark","n":[1]}`,
		"",
	}, "\n")
	if out != want {
		t.Errorf("node output:\n%s\nwant:\n%s", out, want)
	}
}

// An empty struct's decoder type-checks under noUnusedLocals, which the
// harness's tsconfig sets, still refuses a non-object, and answers {}.
func TestEmptyStructDecoder_strictTypeCheck(t *testing.T) {
	r := newTestRegistry()
	r.Types = []wiregen.Type{wiregen.TypeRef[edges.EmptyStruct]()}
	out := runTS(t, r, map[string]string{
		"main.ts": `import { decodeEmptyStruct } from "./decoders.gen.js";
for (const body of ['{}', '{"x":1}', '[]']) {
  try {
    console.log("ok " + JSON.stringify(decodeEmptyStruct(JSON.parse(body))));
  } catch (e) {
    console.log("refused " + (e as Error).message);
  }
}
`,
	})
	if want := "ok {}\nok {}\nrefused $.empty_struct: expected object, got array\n"; out != want {
		t.Errorf("node output:\n%s\nwant:\n%s", out, want)
	}
}

// The arbitraries of named maps type-check under the strict harness: a
// registry whose maps reference nothing names no tie parameter, and recursive
// maps tie to themselves.
func TestMapType_ArbitrariesTypeCheck(t *testing.T) {
	plain := newTestRegistry(wiregen.WithArbitrariesFile("arbitraries.gen.ts"))
	plain.Types = []wiregen.Type{wiregen.TypeRef[maps.Settings](), wiregen.TypeRef[maps.Lists](), wiregen.TypeRef[maps.Ranked]()}
	recursive := newTestRegistry(wiregen.WithArbitrariesFile("arbitraries.gen.ts"))
	recursive.Types = []wiregen.Type{
		wiregen.TypeRef[maps.Tree](), wiregen.TypeRef[maps.Left](), wiregen.TypeRef[maps.Right](),
		wiregen.TypeRef[maps.Node](), wiregen.TypeRef[maps.Kids](),
	}
	for name, r := range map[string]*testRegistry{"no references": plain, "recursive": recursive} {
		t.Run(name, func(t *testing.T) {
			dir := tsProject(t, r, map[string]string{})
			stub := filepath.Join(dir, "node_modules", "fast-check")
			if err := os.MkdirAll(stub, 0o755); err != nil {
				t.Fatal(err)
			}
			for file, body := range map[string]string{
				"package.json": `{"name": "fast-check", "type": "module", "types": "index.d.ts"}`,
				"index.d.ts":   fastCheckStub,
			} {
				if err := os.WriteFile(filepath.Join(stub, file), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if out, err := tool(t, dir, "tsc", "-p", "tsconfig.json", "--noEmit"); err != nil {
				t.Fatalf("tsc: %v\n%s", err, out)
			}
		})
	}
}
