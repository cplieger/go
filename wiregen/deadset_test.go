package wiregen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/wiregen/v2"
	"github.com/cplieger/go/wiregen/v2/testdata/basic"
)

// wireSource is the Go package of edgesProject's target root. Status is read
// by a decoder, so the decoders module declares its value array; Priority is
// read by none, so only its type alias names it. User flattens Base, which
// flattens Meta, the pointer embed *Extra, Stamp and *Note, aliases of
// struct literals, and Mark, an alias of the named struct Badge.
const wireSource = `package wire

type Status string

const (
	StatusActive  Status = "active"
	StatusDefault Status = "active"
	StatusBanned  Status = "banned"
)

type Priority string

const PriorityLow Priority = "low"

type Meta struct {
	Rev int ` + "`json:\"rev\"`" + `
}

type Base struct {
	Meta
	Created string ` + "`json:\"created\"`" + `
}

type Extra struct {
	Flag bool ` + "`json:\"flag\"`" + `
}

type Stamp = struct {
	At string ` + "`json:\"at\"`" + `
}

type Note = struct {
	Text string ` + "`json:\"text\"`" + `
}

type Badge struct {
	Color string ` + "`json:\"color\"`" + `
}

type Mark = Badge

type User struct {
	Base
	*Extra
	Stamp
	*Note
	Mark
	ID     int    ` + "`json:\"id\"`" + `
	Name   string ` + "`json:\"display-name\"`" + `
	Größe  int    ` + "`json:\"größe\"`" + `
	Status Status ` + "`json:\"status\"`" + `
	Tag    string ` + "`json:\"#tag\"`" + `
	One    int    ` + "`json:\"١\"`" + `
	Third  int    ` + "`json:\"٣d\"`" + `
	Label  string ` + "`json:\"display name\"`" + `
}

type Event struct {
	Kind  string ` + "`json:\"kind\"`" + `
	Only  int    ` + "`json:\"only,omitempty\"`" + `
	Both  string ` + "`json:\"both,omitempty\"`" + `
	Other int    ` + "`json:\"other,omitempty\"`" + `
}

type Other struct {
	Note string ` + "`json:\"note\"`" + `
}
`

// edgesProject lays out a deadset target root inside the module, so the go
// command loads its package: the Go package wire, and a web package named
// app whose src/wire is the TS directory. It returns the root and wire's
// import path.
func edgesProject(t *testing.T, manifest string) (root, pkg string) {
	t.Helper()
	root = moduleTempDir(t)
	for _, d := range []string{"wire", filepath.Join("web", "src", "wire")} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "wire", "wire.go"), wireSource)
	if manifest != "" {
		writeFile(t, filepath.Join(root, "web", "package.json"), manifest)
	}
	return root, "github.com/cplieger/go/wiregen/v2/" + filepath.Base(root) + "/wire"
}

func wireRegistry(pkg string, types ...string) *wiregen.Registry {
	r := &wiregen.Registry{Enums: []wiregen.EnumRef{
		{Type: wiregen.Type{PkgPath: pkg, Name: "Status"}},
		{Type: wiregen.Type{PkgPath: pkg, Name: "Priority"}},
	}}
	for _, name := range types {
		r.Types = append(r.Types, wiregen.Type{PkgPath: pkg, Name: name})
	}
	return r
}

// generateEdges runs Generate into root's TS directory with the edges option
// on root and returns the edges document it leaves.
func generateEdges(t *testing.T, root string, r *wiregen.Registry) (string, error) {
	t.Helper()
	return generateEdgesInto(t, root, filepath.Join(root, "web", "src", "wire"), r)
}

func generateEdgesInto(t *testing.T, root, tsDir string, r *wiregen.Registry) (string, error) {
	t.Helper()
	err := r.Generate(t.Context(), wiregen.WithTSDir(tsDir), wiregen.WithDeadsetEdges(root))
	data, _ := os.ReadFile(filepath.Join(root, "deadset-edges.json"))
	return string(data), err
}

func mustGenerateEdges(t *testing.T, root string, r *wiregen.Registry) string {
	t.Helper()
	doc, err := generateEdges(t, root, r)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return doc
}

type edgeSides struct {
	Because  *string `json:"because"`
	Provides string  `json:"provides"`
	UsedBy   string  `json:"used_by"`
}

func readEdges(t *testing.T, doc string) (description string, edges map[string]edgeSides) {
	t.Helper()
	var d struct {
		Description string               `json:"description"`
		Edges       map[string]edgeSides `json:"edges"`
	}
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		t.Fatalf("the edges document does not parse: %v\n%s", err, doc)
	}
	return d.Description, d.Edges
}

// The expected document follows deadset-spec docs/edges.md and
// contract/grammar/symbol-ref.md (a promoted field is its declaring
// struct's), deadset-ts src/ref.ts nameComponent for a member (bare for
// TS_IDENT or '#' and TS_IDENT, else single-quoted, a space included), one
// edge per enum constant, repeats included, paired with its value array or
// else the alias, and one per flattened embed, a field of its declaring
// struct in deadset-go's internal/inventory EmbeddedName, paired with the type.
// deadset-go walkTypeSpec gives an alias of a struct literal its own fields.
func TestDeadsetEdges_PairsEachGeneratedSymbol(t *testing.T) {
	root, pkg := edgesProject(t, `{"name": "app"}`)
	r := wireRegistry(pkg, "User")
	r.Constants = []wiregen.Const{{TSName: "MAX_USERS", Value: 9}}
	got := mustGenerateEdges(t, root, r)

	want := strings.ReplaceAll(`{
  "description": "Edges between Go wire symbols and the TypeScript wiregen generates from them; wiregen rewrites every edge under wiregen/.",
  "edges": {
    "wiregen/Priority": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#Priority",
      "used_by": "ts://app/src/wire/types.gen.ts#Priority"
    },
    "wiregen/Priority/PriorityLow": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#PriorityLow",
      "used_by": "ts://app/src/wire/types.gen.ts#Priority"
    },
    "wiregen/Status": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#Status",
      "used_by": "ts://app/src/wire/types.gen.ts#Status"
    },
    "wiregen/Status/StatusActive": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#StatusActive",
      "used_by": "ts://app/src/wire/decoders.gen.ts#STATUSS"
    },
    "wiregen/Status/StatusBanned": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#StatusBanned",
      "used_by": "ts://app/src/wire/decoders.gen.ts#STATUSS"
    },
    "wiregen/Status/StatusDefault": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#StatusDefault",
      "used_by": "ts://app/src/wire/decoders.gen.ts#STATUSS"
    },
    "wiregen/User": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#User",
      "used_by": "ts://app/src/wire/types.gen.ts#User"
    },
    "wiregen/User/Base": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#User.Base",
      "used_by": "ts://app/src/wire/types.gen.ts#User"
    },
    "wiregen/User/Base.Created": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#Base.Created",
      "used_by": "ts://app/src/wire/types.gen.ts#User.created"
    },
    "wiregen/User/Base.Meta": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#Base.Meta",
      "used_by": "ts://app/src/wire/types.gen.ts#User"
    },
    "wiregen/User/Base.Meta.Rev": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#Meta.Rev",
      "used_by": "ts://app/src/wire/types.gen.ts#User.rev"
    },
    "wiregen/User/Extra": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#User.Extra",
      "used_by": "ts://app/src/wire/types.gen.ts#User"
    },
    "wiregen/User/Extra.Flag": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#Extra.Flag",
      "used_by": "ts://app/src/wire/types.gen.ts#User.flag"
    },
    "wiregen/User/Gr-u0000F6-u0000DFe": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#User.Größe",
      "used_by": "ts://app/src/wire/types.gen.ts#User.größe"
    },
    "wiregen/User/ID": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#User.ID",
      "used_by": "ts://app/src/wire/types.gen.ts#User.id"
    },
    "wiregen/User/Label": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#User.Label",
      "used_by": "ts://app/src/wire/types.gen.ts#User.'display name'"
    },
    "wiregen/User/Mark": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#User.Mark",
      "used_by": "ts://app/src/wire/types.gen.ts#User"
    },
    "wiregen/User/Mark.Color": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#Badge.Color",
      "used_by": "ts://app/src/wire/types.gen.ts#User.color"
    },
    "wiregen/User/Name": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#User.Name",
      "used_by": "ts://app/src/wire/types.gen.ts#User.'display-name'"
    },
    "wiregen/User/Note": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#User.Note",
      "used_by": "ts://app/src/wire/types.gen.ts#User"
    },
    "wiregen/User/Note.Text": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#Note.Text",
      "used_by": "ts://app/src/wire/types.gen.ts#User.text"
    },
    "wiregen/User/One": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#User.One",
      "used_by": "ts://app/src/wire/types.gen.ts#User.١"
    },
    "wiregen/User/Stamp": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#User.Stamp",
      "used_by": "ts://app/src/wire/types.gen.ts#User"
    },
    "wiregen/User/Stamp.At": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#Stamp.At",
      "used_by": "ts://app/src/wire/types.gen.ts#User.at"
    },
    "wiregen/User/Status": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#User.Status",
      "used_by": "ts://app/src/wire/types.gen.ts#User.status"
    },
    "wiregen/User/Tag": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#User.Tag",
      "used_by": "ts://app/src/wire/types.gen.ts#User.#tag"
    },
    "wiregen/User/Third": {
      "because": "generated by wiregen",
      "provides": "go://@PKG@#User.Third",
      "used_by": "ts://app/src/wire/types.gen.ts#User.٣d"
    }
  }
}
`, "@PKG@", pkg)
	if got != want {
		t.Errorf("edges document:\n%s\nwant:\n%s", got, want)
	}

	decoders, err := os.ReadFile(filepath.Join(root, "web", "src", "wire", "decoders.gen.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(decoders), "const STATUSS = [") || strings.Contains(string(decoders), "const PRIORITYS") {
		t.Errorf("the decoders module should declare STATUSS and no PRIORITYS, as the edges assume:\n%s", decoders)
	}
}

// A union's field pairs with its property when one case alone declares it,
// as deadset-ts src/inventory/syntax.ts aliasMembers makes it a member, and
// with the alias when two cases declare it, the tag among them.
func TestDeadsetEdges_PairsUnionFields(t *testing.T) {
	root, pkg := edgesProject(t, `{"name": "app"}`)
	r := wireRegistry(pkg, "Event")
	r.Unions = []wiregen.Union{{
		Type:  wiregen.Type{PkgPath: pkg, Name: "Event"},
		Tag:   "kind",
		Cases: map[string][]string{"a": {"only", "both"}, "b": {"both", "other"}},
	}}
	_, edges := readEdges(t, mustGenerateEdges(t, root, r))
	const alias = "ts://app/src/wire/types.gen.ts#Event"
	for id, want := range map[string]string{
		"wiregen/Event":       alias,
		"wiregen/Event/Kind":  alias,
		"wiregen/Event/Both":  alias,
		"wiregen/Event/Only":  alias + ".only",
		"wiregen/Event/Other": alias + ".other",
	} {
		if got := edges[id].UsedBy; got != want {
			t.Errorf("%s used_by = %q, want %q", id, got, want)
		}
	}
	if got, want := edges["wiregen/Event/Only"].Provides, "go://"+pkg+"#Event.Only"; got != want {
		t.Errorf("wiregen/Event/Only provides = %q, want %q", got, want)
	}
}

// Without a named package.json at or above the TS file, the ts:// package is
// "." and the path is the target root's (symbol-ref.md, "The TypeScript scope").
func TestDeadsetEdges_UnnamedPackageIsTheRoot(t *testing.T) {
	for name, manifest := range map[string]string{"no manifest": "", "a manifest without a name": `{"type": "module"}`} {
		t.Run(name, func(t *testing.T) {
			root, pkg := edgesProject(t, manifest)
			_, edges := readEdges(t, mustGenerateEdges(t, root, wireRegistry(pkg, "User")))
			if got, want := edges["wiregen/User"].UsedBy, "ts://./web/src/wire/types.gen.ts#User"; got != want {
				t.Errorf("wiregen/User used_by = %q, want %q", got, want)
			}
		})
	}
}

// A Go package outside the target root is one deadset never enumerates, so
// an edge naming it would fail as DS9005: it gets none.
func TestDeadsetEdges_SkipsPackagesOutsideTheRoot(t *testing.T) {
	root, pkg := edgesProject(t, `{"name": "app"}`)
	r := wireRegistry(pkg, "User")
	r.Types = append(r.Types, wiregen.TypeRef[basic.Address]())
	_, edges := readEdges(t, mustGenerateEdges(t, root, r))
	for id := range edges {
		if strings.HasPrefix(id, "wiregen/Address") {
			t.Errorf("edge %s names a package outside the target root", id)
		}
	}
	if _, ok := edges["wiregen/User"]; !ok {
		t.Error("wiregen/User is missing")
	}
}

// A merge keeps the description and every edge outside wiregen/ as written,
// escapes included, and replaces wiregen's own.
func TestDeadsetEdges_MergeKeepsHandEdges(t *testing.T) {
	root, pkg := edgesProject(t, `{"name": "app"}`)
	writeFile(t, filepath.Join(root, "deadset-edges.json"), `{
  "description": "m\u0069ne",
  "edges": {
    "app/sse": {"provides": "go://example.com/x#Stream", "used_by": "ts://app/src/sse.ts#\u006Fpen\/"},
    "wiregen/Gone": {"because": "generated by wiregen", "provides": "go://example.com/x#Gone", "used_by": "ts://app/src/wire/types.gen.ts#Gone"}
  }
}`)
	got := mustGenerateEdges(t, root, wireRegistry(pkg, "User"))
	for _, raw := range []string{`"description": "m\u0069ne"`, `"used_by": "ts://app/src/sse.ts#\u006Fpen\/"`} {
		if !strings.Contains(got, raw) {
			t.Errorf("the merge rewrote %s:\n%s", raw, got)
		}
	}
	description, edges := readEdges(t, got)
	if description != "mine" {
		t.Errorf("description = %q, want the hand-written %q", description, "mine")
	}
	hand, ok := edges["app/sse"]
	if !ok || hand.Because != nil || hand.Provides != "go://example.com/x#Stream" || hand.UsedBy != "ts://app/src/sse.ts#open/" {
		t.Errorf("hand edge app/sse = %+v (present %v), want it unchanged", hand, ok)
	}
	if _, ok := edges["wiregen/Gone"]; ok {
		t.Error("the stale generated edge wiregen/Gone survived the merge")
	}
	if _, ok := edges["wiregen/User/ID"]; !ok {
		t.Error("wiregen/User/ID is missing after the merge")
	}
}

// A type no longer registered loses its edge and its fields' edges, so the
// next deadset run sees no stale side (DS9005).
func TestDeadsetEdges_RemovedTypeDropsItsEdges(t *testing.T) {
	root, pkg := edgesProject(t, `{"name": "app"}`)
	_, before := readEdges(t, mustGenerateEdges(t, root, wireRegistry(pkg, "User", "Other")))
	if _, ok := before["wiregen/Other/Note"]; !ok {
		t.Fatal("wiregen/Other/Note is missing while Other is registered")
	}
	_, after := readEdges(t, mustGenerateEdges(t, root, wireRegistry(pkg, "User")))
	for id := range after {
		if strings.HasPrefix(id, "wiregen/Other") {
			t.Errorf("edge %s survived Other's removal", id)
		}
	}
	if _, ok := after["wiregen/User"]; !ok {
		t.Error("wiregen/User is missing")
	}
}

// Regenerating over its own output, with hand edges beside it and the
// registry in another order, writes the same bytes.
func TestDeadsetEdges_RegenerationIsIdempotent(t *testing.T) {
	root, pkg := edgesProject(t, `{"name": "app"}`)
	writeFile(t, filepath.Join(root, "deadset-edges.json"), `{"edges": {"z/b": {"provides": "go://example.com/x#B", "used_by": "ts://app/b.ts#B"}, "a": {"provides": "go://example.com/x#A", "used_by": "ts://app/a.ts#A"}}}`)
	first := mustGenerateEdges(t, root, wireRegistry(pkg, "User", "Other"))
	if !strings.HasSuffix(first, "}\n") {
		t.Errorf("the document should end in a newline:\n%q", first)
	}
	reordered := wireRegistry(pkg, "Other", "User")
	slices.Reverse(reordered.Enums)
	for i := range 3 {
		if again := mustGenerateEdges(t, root, reordered); again != first {
			t.Fatalf("regeneration %d changed the document:\n%s\nfirst:\n%s", i, again, first)
		}
	}
	if a, z := strings.Index(first, `"a"`), strings.Index(first, `"z/b"`); a < 0 || z < a {
		t.Errorf("edges are not sorted by identifier:\n%s", first)
	}
}

// An edge under wiregen/ without wiregen's marker is hand-written, and a
// regeneration would delete it: Generate refuses before writing anything.
func TestDeadsetEdges_RefusesHandEdgesUnderThePrefix(t *testing.T) {
	cases := map[string]string{
		"no because":       `{"provides": "go://example.com/x#A", "used_by": "ts://app/a.ts#A"}`,
		"another because":  `{"because": "kept by hand", "provides": "go://example.com/x#A", "used_by": "ts://app/a.ts#A"}`,
		"a non-string one": `{"because": 1, "provides": "go://example.com/x#A", "used_by": "ts://app/a.ts#A"}`,
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			root, pkg := edgesProject(t, `{"name": "app"}`)
			before := `{"edges": {"wiregen/Mine": ` + e + `, "wiregen/Yours": ` + e + `}}`
			writeFile(t, filepath.Join(root, "deadset-edges.json"), before)
			doc, err := generateEdges(t, root, wireRegistry(pkg, "User"))
			if err == nil || !strings.Contains(err.Error(), "wiregen/Mine, wiregen/Yours") || !strings.Contains(err.Error(), "rename") {
				t.Fatalf("Generate = %v, want a refusal naming wiregen/Mine, wiregen/Yours and asking for a rename", err)
			}
			if doc != before {
				t.Errorf("the refused document was rewritten:\n%s", doc)
			}
			if _, err := os.Stat(filepath.Join(root, "web", "src", "wire", "types.gen.ts")); err == nil {
				t.Error("Generate wrote the types file before refusing")
			}
		})
	}
}

// A document that is not strict JSON, or holds a member or a description
// the edges document (deadset-spec contract/edges.schema.json) has no place
// for, is refused rather than rewritten, and left as it was.
func TestDeadsetEdges_RefusesAnInvalidDocument(t *testing.T) {
	for name, doc := range map[string]string{
		"not JSON":                 `{"edges": `,
		"an unknown member":        `{"edges": {}, "extra": 1}`,
		"a duplicate edge":         `{"edges": {"a": {}, "a": {}}}`,
		"a non-string description": `{"description": 1, "edges": {}}`,
	} {
		t.Run(name, func(t *testing.T) {
			root, pkg := edgesProject(t, `{"name": "app"}`)
			writeFile(t, filepath.Join(root, "deadset-edges.json"), doc)
			got, err := generateEdges(t, root, wireRegistry(pkg, "User"))
			if err == nil || !strings.Contains(err.Error(), "is not a deadset edges document") {
				t.Errorf("Generate = %v, want the document refused", err)
			}
			if got != doc {
				t.Errorf("the refused document was rewritten:\n%s", got)
			}
		})
	}
}

// A null description, which carries nothing, is dropped, and a null or
// absent edges member reads as no edges: the rewritten document is valid.
func TestDeadsetEdges_RepairsAnEmptyDocument(t *testing.T) {
	for name, tc := range map[string]struct{ doc, description string }{
		"a null description": {`{"description": null, "edges": {}}`, ""},
		"null edges":         {`{"description": "mine", "edges": null}`, "mine"},
		"no edges":           {`{"description": "mine"}`, "mine"},
	} {
		t.Run(name, func(t *testing.T) {
			root, pkg := edgesProject(t, `{"name": "app"}`)
			writeFile(t, filepath.Join(root, "deadset-edges.json"), tc.doc)
			got := mustGenerateEdges(t, root, wireRegistry(pkg, "User"))
			description, edges := readEdges(t, got)
			if description != tc.description {
				t.Errorf("description = %q, want %q", description, tc.description)
			}
			if _, ok := edges["wiregen/User"]; !ok {
				t.Errorf("wiregen/User is missing:\n%s", got)
			}
			if strings.Contains(got, "null") {
				t.Errorf("the document still holds a null:\n%s", got)
			}
		})
	}
}

// Generate reads back whatever it wrote, a document past deadset's 1 MiB cap
// (deadset-spec contract/config.schema.json x-max-bytes) included.
func TestDeadsetEdges_ReadsBackALargeDocument(t *testing.T) {
	root, pkg := edgesProject(t, `{"name": "app"}`)
	long := strings.Repeat("x", 1<<20)
	writeFile(t, filepath.Join(root, "deadset-edges.json"), `{"description": "`+long+`", "edges": {}}`)
	first := mustGenerateEdges(t, root, wireRegistry(pkg, "User"))
	if description, _ := readEdges(t, first); description != long {
		t.Errorf("description has %d bytes, want the %d written", len(description), len(long))
	}
	if again := mustGenerateEdges(t, root, wireRegistry(pkg, "User")); again != first {
		t.Error("regenerating over a large document changed it")
	}
}

// symbol-ref.md "The TypeScript scope" spells the package and the path in
// the scope; a byte neither may hold raw is percent-encoded with uppercase
// hex (RFC 3986 section 2.1): in the package, any outside TS_PACKAGE's
// letters, digits, '.', '_' and '-', a scoped name keeping '@' and '/'; in
// the path, whitespace, '#', which ends the scope, and '%' itself.
func TestDeadsetEdges_PercentEncodesTheScope(t *testing.T) {
	for name, tc := range map[string]struct{ manifest, tsDir, want string }{
		"a space in the path":        {`{"name": "app"}`, "my wire", "ts://app/src/my%20wire/types.gen.ts#User"},
		"'#' and '%' in the path":    {`{"name": "app"}`, "a#b%c", "ts://app/src/a%23b%25c/types.gen.ts#User"},
		"a non-ASCII path":           {`{"name": "app"}`, "größe", "ts://app/src/größe/types.gen.ts#User"},
		"a legacy package name":      {`{"name": "left~pad"}`, "wire", "ts://left%7Epad/src/wire/types.gen.ts#User"},
		"a legacy scoped name":       {`{"name": "@legacy!/tool"}`, "wire", "ts://@legacy%21/tool/src/wire/types.gen.ts#User"},
		"a plain scoped name":        {`{"name": "@example/app"}`, "wire", "ts://@example/app/src/wire/types.gen.ts#User"},
		"a space in the root's path": {"", "my wire", "ts://./web/src/my%20wire/types.gen.ts#User"},
	} {
		t.Run(name, func(t *testing.T) {
			root, pkg := edgesProject(t, tc.manifest)
			doc, err := generateEdgesInto(t, root, filepath.Join(root, "web", "src", tc.tsDir), wireRegistry(pkg, "User"))
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			_, edges := readEdges(t, doc)
			if got := edges["wiregen/User"].UsedBy; got != tc.want {
				t.Errorf("wiregen/User used_by = %q, want %q", got, tc.want)
			}
		})
	}
}

// A target root climbing out of a symlink with ".." is the directory the
// operating system reaches, not the lexical parent.
func TestDeadsetEdges_ResolvesTheRootPhysically(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(`Windows resolves ".." lexically, so src\hop\.. is src (TestDeadsetEdges_RootClimbingOutOfASymlinkIsLexicalOnWindows)`)
	}
	root, pkg := edgesProject(t, `{"name": "app"}`)
	hop := filepath.Join(root, "web", "src", "hop")
	if err := os.Symlink(filepath.Join(root, "web"), hop); err != nil {
		t.Fatal(err)
	}
	err := wireRegistry(pkg, "User").Generate(t.Context(), wiregen.WithTSDir(filepath.Join(root, "web", "src", "wire")),
		wiregen.WithDeadsetEdges(hop+string(filepath.Separator)+".."))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "deadset-edges.json"))
	if err != nil {
		t.Fatalf("no edges document at the physical root: %v", err)
	}
	if _, edges := readEdges(t, string(data)); edges["wiregen/User"].UsedBy != "ts://app/src/wire/types.gen.ts#User" {
		t.Errorf("wiregen/User = %+v, want it scoped from the physical root", edges["wiregen/User"])
	}
	if _, err := os.Stat(filepath.Join(root, "web", "src", "deadset-edges.json")); err == nil {
		t.Error("an edges document was written at the lexical parent")
	}
}

// deadset reads TypeScript only below its target root, and the edges file is
// one of Generate's outputs, so neither may be misplaced.
func TestDeadsetEdges_RefusesAMisplacedLayout(t *testing.T) {
	t.Run("a TS directory outside the root", func(t *testing.T) {
		root, pkg := edgesProject(t, `{"name": "app"}`)
		err := wireRegistry(pkg, "User").Generate(t.Context(), wiregen.WithTSDir(t.TempDir()), wiregen.WithDeadsetEdges(filepath.Join(root, "wire")))
		if err == nil || !strings.Contains(err.Error(), "the TS directory") || !strings.Contains(err.Error(), "outside the deadset target root") {
			t.Errorf("Generate = %v, want the TS directory refused as outside the target root", err)
		}
	})
	t.Run("a symlinked edges file", func(t *testing.T) {
		root, pkg := edgesProject(t, `{"name": "app"}`)
		target := filepath.Join(root, "edges.json")
		const content = `{"edges": {}}`
		writeFile(t, target, content)
		if err := os.Symlink(target, filepath.Join(root, "deadset-edges.json")); err != nil {
			t.Fatal(err)
		}
		_, err := generateEdges(t, root, wireRegistry(pkg, "User"))
		if err == nil || !strings.Contains(err.Error(), "is a symbolic link") || !strings.Contains(err.Error(), "regular file") {
			t.Errorf("Generate = %v, want the link refused as not a regular file", err)
		}
		if data, _ := os.ReadFile(target); string(data) != content {
			t.Errorf("the link's target was rewritten:\n%s", data)
		}
	})
	t.Run("a directory at the edges file", func(t *testing.T) {
		root, pkg := edgesProject(t, `{"name": "app"}`)
		if err := os.Mkdir(filepath.Join(root, "deadset-edges.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		before := treeOf(t, root)
		_, err := generateEdges(t, root, wireRegistry(pkg, "User"))
		if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
			t.Errorf("Generate = %v, want the directory refused as not a regular file", err)
		}
		if after := treeOf(t, root); !slices.Equal(after, before) {
			t.Errorf("the tree changed:\n%v\nwant:\n%v", after, before)
		}
	})
	t.Run("another output at the edges file", func(t *testing.T) {
		root, pkg := edgesProject(t, `{"name": "app"}`)
		err := wireRegistry(pkg, "User").Generate(t.Context(), wiregen.WithTSDir(filepath.Join(root, "web", "src", "wire")),
			wiregen.WithDeadsetEdges(root), wiregen.WithOpenAPIFiles(filepath.Join(root, "deadset-edges.json")))
		if err == nil || !strings.Contains(err.Error(), "the openapi and deadset-edges files") {
			t.Errorf("Generate = %v, want the output collision refused", err)
		}
	})
}

// The option is off by default, so a registry that never asks writes no
// edges document.
func TestDeadsetEdges_OffByDefault(t *testing.T) {
	root, pkg := edgesProject(t, `{"name": "app"}`)
	if err := wireRegistry(pkg, "User").Generate(t.Context(), wiregen.WithTSDir(filepath.Join(root, "web", "src", "wire"))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "deadset-edges.json")); err == nil {
		t.Error("an edges document was written without WithDeadsetEdges")
	}
}

// treeOf lists every path below root with its file mode, so a refusal can be
// checked to leave the tree as it was.
func treeOf(t *testing.T, root string) []string {
	t.Helper()
	var tree []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		tree = append(tree, p+" "+fi.Mode().String())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
