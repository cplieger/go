package wiregen_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/wiregen/v2"
	"github.com/cplieger/go/wiregen/v2/testdata/basic"
)

var update = flag.Bool("update", false, "update golden files")

// Ensure types are importable (compile-time check).
var (
	_ basic.User
	_ basic.Address
	_ basic.Notification
	_ basic.HasUnexported
	_ basic.HasBytes
	_ basic.HasOmitzero
	_ basic.HasJSONString
	_ basic.HasCustomMapped
	_ basic.HasTime
	_ basic.HasRaw
	_ basic.HasMap
	_ basic.WithEmbedding
)

func newRegistry() *testRegistry {
	r := newTestRegistry(
		wiregen.WithValidatorsImport("./test-validators.js"),
	)
	r.Types = []wiregen.Type{
		wiregen.TypeRef[basic.Address](),
		wiregen.TypeRef[basic.User](),
		wiregen.TypeRef[basic.Notification](),
	}
	r.Enums = []wiregen.EnumRef{wiregen.StringEnum[basic.Status]()}
	r.Events = []wiregen.Event{
		r.event("notification", "Notification"),
	}
	return r
}

func TestGenerateTypes(t *testing.T) {
	r := newRegistry()
	out := mustGen(t, r.typesFile)

	if !strings.Contains(out, `export type Status = "active" | "inactive" | "banned";`) {
		t.Errorf("missing Status enum type, got:\n%s", out)
	}
	if !strings.Contains(out, "export interface User {") {
		t.Errorf("missing User interface, got:\n%s", out)
	}
	if !strings.Contains(out, "  id: number;") {
		t.Errorf("missing required id field, got:\n%s", out)
	}
	if !strings.Contains(out, "  email?: string;") {
		t.Errorf("missing optional email field, got:\n%s", out)
	}
	if !strings.Contains(out, "  address: Address;") {
		t.Errorf("missing nested struct field, got:\n%s", out)
	}
	if !strings.Contains(out, "  tags?: string[];") {
		t.Errorf("missing optional tags field, got:\n%s", out)
	}
	if !strings.Contains(out, "export interface Address {") {
		t.Errorf("missing Address interface, got:\n%s", out)
	}
}

func TestGenerateDecoders(t *testing.T) {
	r := newRegistry()
	out := mustGen(t, r.decodersFile)

	if !strings.Contains(out, "export const decodeUser: Decoder<User>") {
		t.Errorf("missing decodeUser, got:\n%s", out)
	}
	if !strings.Contains(out, "export const decodeAddress: Decoder<Address>") {
		t.Errorf("missing decodeAddress, got:\n%s", out)
	}
	if !strings.Contains(out, "reqOneOf(o, \"status\", STATUSS") {
		t.Errorf("missing enum validation for status, got:\n%s", out)
	}
	if !strings.Contains(out, "decodeAddress(own(o, \"address\"))") {
		t.Errorf("missing nested struct decoder call, got:\n%s", out)
	}
	if !strings.Contains(out, `from "./test-validators.js"`) {
		t.Errorf("missing validators import, got:\n%s", out)
	}
}

func TestGenerateEvents(t *testing.T) {
	r := newRegistry()
	out := mustGen(t, r.eventsFile)
	for _, want := range []string{
		`  | { readonly event: "notification"; readonly data: Notification }`,
		`  notification: decodeNotification,`,
		`    case "notification":` + "\n" + `      return { event: "notification", data: decodeNotification(data) };`,
		"import type { Notification } from \"./types.gen.js\";",
		"throw new globalThis.TypeError(`unknown event ${globalThis.JSON.stringify(event)}`);",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("events module missing %q, got:\n%s", want, out)
		}
	}
}

// TestGenerateEvents_DataTypeResolvedAsIfInTypes pins Registry's contract
// that an event's data type is generated as if listed in Types, so any data
// type name reaches the same resolution and refusals.
func TestGenerateEvents_DataTypeResolvedAsIfInTypes(t *testing.T) {
	pkg := wiregen.TypeRef[basic.Address]().PkgPath
	cases := []struct {
		name, typeName, wantErr string
	}{
		{"missing from its package", "Missing", "type Missing not found"},
		{"a registered enum's name", "Status", "is registered twice"},
		{"a string type", "Priority", "is not a struct"},
		{"a constant", "StatusActive", "StatusActive in " + pkg + " is not a type"},
		{"no name", "", "names no package-level type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRegistry()
			r.Enums = []wiregen.EnumRef{wiregen.StringEnum[basic.Status]()}
			r.Events = []wiregen.Event{{Name: "e", Data: wiregen.Type{PkgPath: pkg, Name: tc.typeName}}}
			requireGenerateError(t, r, tc.wantErr)
		})
	}
	t.Run("an unlisted struct", func(t *testing.T) {
		events := []wiregen.Event{{Name: "e", Data: wiregen.Type{PkgPath: pkg, Name: "Address"}}}
		unlisted := newTestRegistry()
		unlisted.Events = events
		listed := newTestRegistry()
		listed.Types = []wiregen.Type{wiregen.TypeRef[basic.Address]()}
		listed.Events = events
		files := []struct {
			name             string
			unlisted, listed func(context.Context) (string, error)
		}{
			{"types", unlisted.typesFile, listed.typesFile},
			{"decoders", unlisted.decodersFile, listed.decodersFile},
		}
		for _, fc := range files {
			if got, want := mustGen(t, fc.unlisted), mustGen(t, fc.listed); got != want {
				t.Errorf("%s file with Address only as event data differs from Address in Types:\ngot:\n%s\nwant:\n%s", fc.name, got, want)
			}
		}
	})
}

func TestGenerateToDir(t *testing.T) {
	r := newRegistry()
	dir := t.TempDir()
	if err := r.generate(t.Context(), dir); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	for _, f := range []string{"types.gen.ts", "decoders.gen.ts", "events.gen.ts"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected file %s to exist: %v", f, err)
		}
	}
}

// An output in a folder that does not exist yet is created with its folders,
// a TS file named under a subfolder of the TS directory and an OpenAPI file
// beside it alike, on every platform: Windows refuses filesafe's
// mode-proving option, so the folders are created like os.MkdirAll.
func TestGenerate_CreatesMissingOutputFolders(t *testing.T) {
	r := fullReg()
	dir := t.TempDir()
	openapi := filepath.Join(dir, "api", "v1", "openapi.json")
	err := r.Generate(t.Context(), append(r.opts,
		wiregen.WithTSDir(dir),
		wiregen.WithTypesFile("gen/wire/types.gen.ts"),
		wiregen.WithOpenAPIFiles(openapi))...)
	if err != nil {
		t.Fatalf("Generate with outputs in missing folders failed: %v", err)
	}
	for _, f := range []string{filepath.Join(dir, "gen", "wire", "types.gen.ts"), openapi} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("expected %s to exist: %v", f, err)
		}
	}
}

// moduleTempDir is a fresh directory inside this package, removed at test end.
// A test that changes the working directory stays inside the module, because
// Generate loads the registered packages from the working directory, as the
// go command a build-time program runs does.
func moduleTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", "_chdir-")
	if errors.Is(err, fs.ErrPermission) {
		t.Skip("the module is read-only, as in the module cache:", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// A relative WithOpenAPIFiles path is the working directory's, as the option
// documents, in a folder that exists or not; filesafe writes only absolute
// paths, so wiregen resolves it first.
//
// Serial (no t.Parallel): t.Chdir changes process state.
func TestGenerate_RelativeOpenAPIPathsAreTheWorkingDirectorys(t *testing.T) {
	r := fullReg()
	dir := moduleTempDir(t)
	t.Chdir(dir)
	err := r.Generate(t.Context(), append(r.opts,
		wiregen.WithTSDir("ts"),
		wiregen.WithOpenAPIFiles(filepath.Join("api", "v1", "openapi.json"), "top.json"))...)
	if err != nil {
		t.Fatalf("Generate with relative OpenAPI paths failed: %v", err)
	}
	for _, f := range []string{filepath.Join(dir, "api", "v1", "openapi.json"), filepath.Join(dir, "top.json")} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("expected %s to exist: %v", f, err)
		}
	}
}

// A relative OpenAPI path from a working directory reached through a symlink
// resolves as the operating system resolves it, so the collision checks see
// the same file the write lands in. Unix resolves ".." physically, from the
// directory the link points to, not the one $PWD names. Windows keeps the
// working directory as named and resolves ".." lexically, so there ..\ts is
// the TS directory itself and Generate refuses the collision.
//
// Serial (no t.Parallel): t.Chdir and t.Setenv change process state.
func TestGenerate_RelativeOpenAPIPathsResolveAsTheOSDoes(t *testing.T) {
	r := fullReg()
	base := moduleTempDir(t)
	real := filepath.Join(base, "real", "sub")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(link)
	t.Setenv("PWD", link)
	tsDir := filepath.Join(base, "ts")
	err := r.Generate(t.Context(), append(r.opts,
		wiregen.WithTSDir(tsDir),
		wiregen.WithOpenAPIFiles(filepath.Join("..", "ts", "types.gen.ts")))...)
	if runtime.GOOS == "windows" {
		if err == nil || !strings.Contains(err.Error(), "the types and openapi files") {
			t.Fatalf("Generate = %v, want a refusal naming the types and openapi files", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("Generate = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(tsDir, "types.gen.ts"))
	if err != nil || strings.HasPrefix(string(got), "{") {
		t.Fatalf("the TS directory's types.gen.ts = %.40q, %v; want the TypeScript output", got, err)
	}
	if _, err := os.Stat(filepath.Join(base, "real", "ts", "types.gen.ts")); err != nil {
		t.Fatalf("the OpenAPI file at the physical ../ts: %v", err)
	}
}

func TestGenerateConstants(t *testing.T) {
	r := newTestRegistry()
	r.Constants = []wiregen.Const{
		{TSName: "MSG_SCREEN", Value: 0},
		{TSName: "MSG_SCROLL", Value: 1},
		{TSName: "MODE_FLAG_BRACKETED_PASTE", Value: 0x01},
	}
	out := mustGen(t, r.constantsFile)

	if !strings.Contains(out, "// Code generated by wiregen. DO NOT EDIT.") {
		t.Errorf("missing header comment, got:\n%s", out)
	}
	if !strings.Contains(out, "export const MSG_SCREEN = 0;") {
		t.Errorf("missing MSG_SCREEN constant, got:\n%s", out)
	}
	if !strings.Contains(out, "export const MSG_SCROLL = 1;") {
		t.Errorf("missing MSG_SCROLL constant, got:\n%s", out)
	}
	if !strings.Contains(out, "export const MODE_FLAG_BRACKETED_PASTE = 1;") {
		t.Errorf("missing MODE_FLAG_BRACKETED_PASTE constant, got:\n%s", out)
	}
}

func TestGenerateConstants_Empty(t *testing.T) {
	r := newRegistry()
	dir := t.TempDir()
	if err := r.generate(t.Context(), dir); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	_, err := os.Stat(filepath.Join(dir, "constants.gen.ts"))
	if !os.IsNotExist(err) {
		t.Errorf("expected constants.gen.ts to not exist when Constants is empty, but it does")
	}
}

// TestValidatorsImportDefaultsToValidatorsFile pins that with no
// WithValidatorsImport each module imports the validators module, and the
// others, by its relative path from that module's own file.
func TestValidatorsImportDefaultsToValidatorsFile(t *testing.T) {
	r := newTestRegistry(wiregen.WithValidatorsFile("lib/v.gen.ts"),
		wiregen.WithDecodersFile("wire/decoders.gen.ts"), wiregen.WithEventsFile("wire/sse/events.gen.ts"))
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.Notification]()}
	r.Events = []wiregen.Event{r.event("a", "Notification")}
	for role, wants := range map[string][]string{
		"decoders": {`from "../lib/v.gen.js";`, `from "../types.gen.js";`},
		"events":   {`from "../../lib/v.gen.js";`, `from "../decoders.gen.js";`, `from "../../types.gen.js";`},
	} {
		out := mustGen(t, func(ctx context.Context) (string, error) { return r.file(ctx, role) })
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("%s file lacks %s; got:\n%s", role, want, out)
			}
		}
	}
}

func TestUnexportedFieldsSkipped(t *testing.T) {
	r := newTestRegistry(
		wiregen.WithValidatorsImport("./v.js"),
	)
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.HasUnexported]()}
	out := mustGen(t, r.typesFile)
	if !strings.Contains(out, "  name: string;") {
		t.Errorf("exported field 'name' should be present, got:\n%s", out)
	}
	if strings.Contains(out, "internal") || strings.Contains(out, "hidden") {
		t.Errorf("unexported fields should NOT appear in output, got:\n%s", out)
	}
}

func TestByteSliceToString(t *testing.T) {
	r := newTestRegistry(
		wiregen.WithValidatorsImport("./v.js"),
	)
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.HasBytes]()}
	out := mustGen(t, r.typesFile)
	if !strings.Contains(out, "  data: string;") {
		t.Errorf("[]byte should map to string, got:\n%s", out)
	}
	if strings.Contains(out, "number[]") {
		t.Errorf("[]byte should NOT map to number[], got:\n%s", out)
	}
	dec := mustGen(t, r.decodersFile)
	if !strings.Contains(dec, "reqStr(o, \"data\"") {
		t.Errorf("[]byte decoder should use reqStr, got:\n%s", dec)
	}
}

func TestOmitzeroMakesOptional(t *testing.T) {
	r := newTestRegistry(
		wiregen.WithValidatorsImport("./v.js"),
	)
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.HasOmitzero]()}
	out := mustGen(t, r.typesFile)
	if !strings.Contains(out, "  value?: string;") {
		t.Errorf("omitzero field should be optional, got:\n%s", out)
	}
	if !strings.Contains(out, "  name: string;") {
		t.Errorf("non-omitzero field should be required, got:\n%s", out)
	}
}

func TestJSONStringTag(t *testing.T) {
	r := newTestRegistry(
		wiregen.WithValidatorsImport("./v.js"),
	)
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.HasJSONString]()}
	out := mustGen(t, r.typesFile)
	if !strings.Contains(out, "  big_id: string;") {
		t.Errorf("json:\",string\" field should be typed as string, got:\n%s", out)
	}
	dec := mustGen(t, r.decodersFile)
	if !strings.Contains(dec, "reqStr(o, \"big_id\"") {
		t.Errorf("json:\",string\" decoder should use reqStr, got:\n%s", dec)
	}
}

func TestMappings(t *testing.T) {
	r := newTestRegistry(
		wiregen.WithValidatorsImport("./v.js"),
	)
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.HasCustomMapped]()}
	r.Mappings = map[string]wiregen.Mapping{
		"github.com/cplieger/go/wiregen/v2/testdata/basic.CustomID": {TS: "string", Decoder: "reqStr"},
	}
	out := mustGen(t, r.typesFile)
	if !strings.Contains(out, "  id: string;") {
		t.Errorf("the mapping should type CustomID as string, got:\n%s", out)
	}
	dec := mustGen(t, r.decodersFile)
	if !strings.Contains(dec, "reqStr(o, \"id\"") {
		t.Errorf("the mapping's decoder should decode CustomID, got:\n%s", dec)
	}
}

// TestMappings_CollectionElement pins the mapped-element routing: a
// slice of a mapped type decodes per element through
// decodeArray — never as a whole-field scalar mapped call, which would hand
// the raw array to a scalar decoder — and the element decoder's synthesized
// single-key object carries the real wire name and type path so its error
// messages locate the actual field (the index prefix comes from decodeArray).
func TestMappings_CollectionElement(t *testing.T) {
	r := newTestRegistry(
		wiregen.WithValidatorsImport("./v.js"),
	)
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.HasMappedSlice]()}
	r.Mappings = map[string]wiregen.Mapping{
		"github.com/cplieger/go/wiregen/v2/testdata/basic.CustomID": {TS: "string", Decoder: "reqStr"},
	}
	out := mustGen(t, r.typesFile)
	if !strings.Contains(out, "  ids: string[];") {
		t.Errorf("mapped slice element should type as string[], got:\n%s", out)
	}
	dec := mustGen(t, r.decodersFile)
	want := `ids: own(o, "ids") === null ? [] : decodeArray(own(o, "ids"), (v) => reqStr({"ids": v} as globalThis.Record<string, unknown>, "ids", "$.has_mapped_slice"), "$.has_mapped_slice.ids"),`
	if !strings.Contains(dec, want) {
		t.Errorf("mapped slice should decode per element, missing %q, got:\n%s", want, dec)
	}
	if strings.Contains(dec, `ids: reqStr(o, "ids"`) {
		t.Errorf("mapped slice must not decode as a whole-field scalar mapped call, got:\n%s", dec)
	}
}

func TestMappingsWithoutDecoder(t *testing.T) {
	r := newTestRegistry(
		wiregen.WithValidatorsImport("./v.js"),
	)
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.HasCustomMapped]()}
	r.Mappings = map[string]wiregen.Mapping{
		"github.com/cplieger/go/wiregen/v2/testdata/basic.CustomID": {TS: "string"},
	}
	dec := mustGen(t, r.decodersFile)
	if !strings.Contains(dec, "own(o, \"id\") as string") {
		t.Errorf("a mapping without a decoder should cast, got:\n%s", dec)
	}
}

func TestTypesImportPath(t *testing.T) {
	r := newTestRegistry(
		wiregen.WithValidatorsImport("./v.js"),
		wiregen.WithTypesImport("../shared/types.gen.js"),
	)
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.User](), wiregen.TypeRef[basic.Address]()}
	r.Enums = []wiregen.EnumRef{wiregen.StringEnum[basic.Status]()}
	dec := mustGen(t, r.decodersFile)
	if !strings.Contains(dec, `from "../shared/types.gen.js"`) {
		t.Errorf("should use custom TypesImportPath, got:\n%s", dec)
	}
	if strings.Contains(dec, `from "./types.gen.js"`) {
		t.Errorf("should NOT use default types import path, got:\n%s", dec)
	}
}

func TestOptions_Defaults(t *testing.T) {
	r := newTestRegistry()
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.Address]()}
	r.Enums = []wiregen.EnumRef{wiregen.StringEnum[basic.Status]()}
	out := mustGen(t, r.typesFile)
	if !strings.Contains(out, "// Code generated by wiregen. DO NOT EDIT.") {
		t.Errorf("expected default header comment, got:\n%s", out)
	}
}

func TestOptions_FileNames(t *testing.T) {
	r := newTestRegistry(
		wiregen.WithValidatorsImport("./v.js"),
		wiregen.WithTransportImport("./t.js"),
		wiregen.WithClientFile("my_client.ts"),
		wiregen.WithTypesFile("my_types.ts"), wiregen.WithDecodersFile("my_decoders.ts"), wiregen.WithEventsFile("my_events.ts"), wiregen.WithConstantsFile("my_consts.ts"),
	)
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.Address]()}
	r.Events = []wiregen.Event{r.event("addr", "Address")}
	r.Constants = []wiregen.Const{{TSName: "X", Value: 1}}
	r.Endpoints = []wiregen.Endpoint{{Name: "getAddr", Method: "GET", Path: "/api/addr", Kind: wiregen.KindJSON, Response: wiregen.TypeRef[basic.Address]()}}
	dir := t.TempDir()
	if err := r.generate(t.Context(), dir); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	for _, f := range []string{"my_types.ts", "my_decoders.ts", "my_events.ts", "my_consts.ts", "my_client.ts"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected file %s to exist: %v", f, err)
		}
	}
}

// TestFileOptions_CrossImportsFollowTheNames pins the module specifiers the
// generated files use to import each OTHER against the renamed files. A
// renamed decoders file that the events module still imports as
// "./decoders.gen.js", or a renamed types file the decoders still import as
// "./types.gen.js", is a generated tree that does not resolve — the exact case
// the file options exist for, and the case the file-existence checks above miss.
func TestFileOptions_CrossImportsFollowTheNames(t *testing.T) {
	newReg := func() *testRegistry {
		r := newTestRegistry(
			wiregen.WithValidatorsImport("./v.js"),
			wiregen.WithTypesFile("my_types.ts"), wiregen.WithDecodersFile("my_decoders.ts"),
		)
		r.Types = []wiregen.Type{wiregen.TypeRef[basic.Notification]()}
		r.Events = []wiregen.Event{r.event("notif", "Notification")}
		return r
	}
	t.Run("eventsImportDecoders", func(t *testing.T) {
		out := mustGen(t, newReg().eventsFile)
		if !strings.Contains(out, `from "./my_decoders.js"`) {
			t.Errorf("events module = missing `from \"./my_decoders.js\"`, got:\n%s", out)
		}
		if strings.Contains(out, "decoders.gen.js") {
			t.Errorf("events module = still names the default decoders module, got:\n%s", out)
		}
	})
	t.Run("decodersImportTypes", func(t *testing.T) {
		out := mustGen(t, newReg().decodersFile)
		if !strings.Contains(out, `from "./my_types.js"`) {
			t.Errorf("GenerateDecoders() = missing `from \"./my_types.js\"`, got:\n%s", out)
		}
		if strings.Contains(out, "types.gen.js") {
			t.Errorf("GenerateDecoders() = still names the default types module, got:\n%s", out)
		}
	})
	t.Run("explicitTypesImportPathWins", func(t *testing.T) {
		r := newReg()
		r.opts = append(r.opts, wiregen.WithTypesImport("../shared/types.js"))
		out := mustGen(t, r.decodersFile)
		if !strings.Contains(out, `from "../shared/types.js"`) {
			t.Errorf("GenerateDecoders() = explicit TypesImportPath ignored, got:\n%s", out)
		}
	})
}

// TestFileOptions_ModuleSpecifierExtensions pins the source-to-module
// extension rewrite for the TypeScript extensions a consumer can legitimately
// use: .mts and .cts emit .mjs and .cjs, every other extension (and a name
// with none) emits .js.
func TestFileOptions_ModuleSpecifierExtensions(t *testing.T) {
	for _, tc := range []struct{ decoders, want string }{
		{"my_decoders.ts", `from "./my_decoders.js"`},
		{"my_decoders.tsx", `from "./my_decoders.js"`},
		{"my_decoders.mts", `from "./my_decoders.mjs"`},
		{"my_decoders.cts", `from "./my_decoders.cjs"`},
		{"my_decoders", `from "./my_decoders.js"`},
	} {
		t.Run(tc.decoders, func(t *testing.T) {
			r := newTestRegistry(
				wiregen.WithValidatorsImport("./v.js"),
				wiregen.WithDecodersFile(tc.decoders),
			)
			r.Types = []wiregen.Type{wiregen.TypeRef[basic.Notification]()}
			r.Events = []wiregen.Event{r.event("notif", "Notification")}
			out := mustGen(t, r.eventsFile)
			if !strings.Contains(out, tc.want) {
				t.Errorf("events module with Decoders=%q = missing %s, got:\n%s", tc.decoders, tc.want, out)
			}
		})
	}
}

func TestOptions_NilIgnored(t *testing.T) {
	r := newTestRegistry(nil, nil, wiregen.WithValidatorsImport("./v.js"), nil)
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.Address]()}
	if out := mustGen(t, r.typesFile); out == "" {
		t.Error("nil options should be ignored and still generate output")
	}
}

func TestOptions_OrderIndependent(t *testing.T) {
	r1 := newTestRegistry(
		wiregen.WithValidatorsImport("./v.js"),
		wiregen.WithTransportImport("./t.js"),
		wiregen.WithHeaderComment("// H\n\n"),
	)
	r2 := newTestRegistry(
		wiregen.WithHeaderComment("// H\n\n"),
		wiregen.WithTransportImport("./t.js"),
		wiregen.WithValidatorsImport("./v.js"),
	)
	r1.Types = []wiregen.Type{wiregen.TypeRef[basic.Address]()}
	r2.Types = []wiregen.Type{wiregen.TypeRef[basic.Address]()}
	if mustGen(t, r1.typesFile) != mustGen(t, r2.typesFile) {
		t.Error("option order should not affect output")
	}
}

func TestOptions_LastWriterWins(t *testing.T) {
	r := newTestRegistry(
		wiregen.WithHeaderComment("// first\n\n"),
		wiregen.WithHeaderComment("// second\n\n"),
	)
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.Address]()}
	out := mustGen(t, r.typesFile)
	if !strings.Contains(out, "// second") {
		t.Errorf("last writer should win, got:\n%s", out)
	}
}

func TestFileOptions_EmptyKeepsDefaults(t *testing.T) {
	r := newTestRegistry(
		wiregen.WithValidatorsImport("./v.js"),
		wiregen.WithTypesFile(""), wiregen.WithDecodersFile(""), wiregen.WithEventsFile(""), wiregen.WithConstantsFile(""),
	)
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.Address]()}
	r.Events = []wiregen.Event{r.event("a", "Address")}
	r.Constants = []wiregen.Const{{TSName: "X", Value: 1}}
	dir := t.TempDir()
	if err := r.generate(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"types.gen.ts", "decoders.gen.ts", "events.gen.ts", "constants.gen.ts"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("an empty file name should keep default %s: %v", f, err)
		}
	}
}

// --- JSDoc/Comment tests ---

func TestJSDocPassthrough(t *testing.T) {
	r := newTestRegistry(wiregen.WithValidatorsImport("./v.js"))
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.User](), wiregen.TypeRef[basic.Address]()}
	r.Enums = []wiregen.EnumRef{wiregen.StringEnum[basic.Status]()}
	out := mustGen(t, r.typesFile)
	if !strings.Contains(out, "/**") {
		t.Errorf("expected JSDoc comment in output, got:\n%s", out)
	}
}

// --- Golden file tests ---

func goldenRegistry() *testRegistry {
	r := newRegistry()
	r.Types = append(r.Types,
		wiregen.TypeRef[basic.HasTime](),
		wiregen.TypeRef[basic.HasBytes](),
		wiregen.TypeRef[basic.HasMap](),
		wiregen.TypeRef[basic.WithEmbedding](),
	)
	return r
}

func TestGolden_Types(t *testing.T) {
	goldenCompare(t, "testdata/golden/types.gen.ts", mustGen(t, goldenRegistry().typesFile))
}

func TestGolden_Decoders(t *testing.T) {
	goldenCompare(t, "testdata/golden/decoders.gen.ts", mustGen(t, goldenRegistry().decodersFile))
}

func goldenCompare(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden file %s not found (run go test -run TestGolden -update to create): %v", path, err)
	}
	if string(want) != got {
		t.Errorf("output differs from golden file %s (regenerate with go test -run TestGolden -update).\nGot:\n%s\nWant:\n%s", path, got, string(want))
	}
}

// ExampleRegistry_Generate demonstrates basic wiregen usage.
func ExampleRegistry_Generate() {
	dir, err := os.MkdirTemp("", "wiregen-example-")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(dir)
	r := wiregen.Registry{Types: []wiregen.Type{wiregen.TypeRef[basic.Address]()}}
	if err := r.Generate(context.Background(), wiregen.WithTSDir(dir)); err != nil {
		fmt.Println("generate:", err)
		return
	}
	_, err = os.Stat(filepath.Join(dir, "types.gen.ts"))
	fmt.Println(err == nil)
	// Output:
	// true
}

// --- context cancellation ---

// TestGenerate_ContextCancellation pins that the caller can stop the package
// load, a go subprocess, and that the cancellation reason survives in the
// error chain: go/packages formats a driver failure into a fresh error
// without %w, so only newASTEngine's context.Cause keeps errors.Is working.
func TestGenerate_ContextCancellation(t *testing.T) {
	newReg := func() *testRegistry {
		r := newTestRegistry(wiregen.WithValidatorsImport("./v.js"))
		r.Types = []wiregen.Type{wiregen.TypeRef[basic.Address]()}
		return r
	}

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		// Repeated because the two error shapes go/packages can return on a
		// cancelled load are racy.
		for i := range 20 {
			if _, err := newReg().typesFile(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("run %d: render(cancelled ctx) error = %v, want one matching context.Canceled", i, err)
			}
		}
	})

	t.Run("deadlineExceeded", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		defer cancel()
		if _, err := newReg().typesFile(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("render(expired ctx) error = %v, want one matching context.DeadlineExceeded", err)
		}
	})

	t.Run("cancelCauseReachesTheCaller", func(t *testing.T) {
		abort := errors.New("codegen aborted by the caller")
		ctx, cancel := context.WithCancelCause(t.Context())
		cancel(abort)
		if err := newReg().generate(ctx, t.TempDir()); !errors.Is(err, abort) {
			t.Errorf("Generate(cause-cancelled ctx) error = %v, want one matching the cause", err)
		}
	})

	t.Run("noWriteOnACancelledGenerate", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		dir := t.TempDir()
		if err := newReg().generate(ctx, dir); err == nil {
			t.Fatal("Generate(cancelled ctx) error = nil, want non-nil")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", dir, err)
		}
		if len(entries) != 0 {
			t.Errorf("Generate(cancelled ctx) wrote %d entries into the TS dir, want 0", len(entries))
		}
	})
}

// --- TypeRef registration ---
func TestTypeRef(t *testing.T) {
	if wt := wiregen.TypeRef[basic.User](); wt.Name != "User" ||
		wt.PkgPath != "github.com/cplieger/go/wiregen/v2/testdata/basic" {
		t.Errorf("TypeRef[User]() = %+v, want {basic, User}", wt)
	}
	if wt := wiregen.TypeRef[*basic.Address](); wt.Name != "Address" {
		t.Errorf("TypeRef[*Address]().Name = %q, want %q", wt.Name, "Address")
	}
}

// --- Generate error paths ---

func TestGenerate_UnknownTypeErrors(t *testing.T) {
	r := newTestRegistry(wiregen.WithValidatorsImport("./v.js"))
	r.Types = []wiregen.Type{
		{PkgPath: "github.com/cplieger/go/wiregen/v2/testdata/basic", Name: "NonExistentType"},
	}
	err := r.generate(t.Context(), t.TempDir())
	if err == nil {
		t.Fatal("expected error when referencing non-existent type, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

// TestGenerate_RejectsDuplicateTypeNames pins the bare-name keying: the same
// type registered twice and two same-named types from different packages are
// both configuration errors, each with its own remedy in the message.
func TestGenerate_RejectsDuplicateTypeNames(t *testing.T) {
	const (
		crossrefPkg = "github.com/cplieger/go/wiregen/v2/testdata/crossref"
		depPkg      = crossrefPkg + "/dep"
	)
	cases := []struct {
		name    string
		types   []wiregen.Type
		wantErr string
	}{
		{
			name: "same_type_registered_twice",
			types: []wiregen.Type{
				{PkgPath: crossrefPkg, Name: "Item"},
				{PkgPath: crossrefPkg, Name: "Item"},
			},
			wantErr: "is registered twice",
		},
		{
			name: "one_name_from_two_packages",
			types: []wiregen.Type{
				{PkgPath: crossrefPkg, Name: "Color"},
				{PkgPath: depPkg, Name: "Color"},
			},
			wantErr: "registered from two packages",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRegistry(wiregen.WithValidatorsImport("./v.js"))
			r.Types = tc.types
			err := r.generate(t.Context(), t.TempDir())
			if err == nil {
				t.Fatalf("Generate() = nil error, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Generate() error = %q, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestGenerate_PackageLoadFailureErrors(t *testing.T) {
	r := newTestRegistry(wiregen.WithValidatorsImport("./v.js"))
	r.Types = []wiregen.Type{
		{PkgPath: "github.com/totally/nonexistent/package/path", Name: "Foo"},
	}
	if err := r.generate(t.Context(), t.TempDir()); err == nil {
		t.Fatal("expected error for non-existent package, got nil")
	}
}

// TestGenerate_RejectsUnsanitizableConstant: a Const whose TSName
// sanitizes to an empty TS identifier fails Generate, which writes nothing.
func TestGenerate_RejectsUnsanitizableConstant(t *testing.T) {
	r := newTestRegistry(wiregen.WithValidatorsImport("./v.js"))
	r.Constants = []wiregen.Const{
		{TSName: "MaxRetries", Value: 3},
		{TSName: "404", Value: 5}, // digits only: sanitizes to ""
	}
	dir := t.TempDir()
	err := r.generate(t.Context(), dir)
	if err == nil {
		t.Fatal("expected error for unsanitizable constant TSName, got nil")
	}
	if !strings.Contains(err.Error(), `"404"`) {
		t.Errorf("error should name the offending TSName, got: %v", err)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("ReadDir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("Generate must write nothing on a constants validation error, found %d entries", len(entries))
	}
}

// --- empty / constants-only registries ---

func TestGenerate_EmptyRegistry(t *testing.T) {
	r := newTestRegistry(wiregen.WithValidatorsImport("./v.js"))
	if err := r.generate(t.Context(), t.TempDir()); err != nil {
		t.Fatalf("empty registry should generate fine, got: %v", err)
	}
}

func TestGenerate_ConstantsOnly(t *testing.T) {
	r := newTestRegistry(wiregen.WithValidatorsImport("./v.js"))
	r.Constants = []wiregen.Const{
		{TSName: "FOO", Value: 42},
		{TSName: "BAR", Value: -1},
	}
	dir := t.TempDir()
	if err := r.generate(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "constants.gen.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "export const FOO = 42;") {
		t.Errorf("missing FOO, got:\n%s", content)
	}
	if !strings.Contains(string(content), "export const BAR = -1;") {
		t.Errorf("missing BAR, got:\n%s", content)
	}
}
