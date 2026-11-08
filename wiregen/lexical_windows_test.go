package wiregen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/go/wiregen"
	"github.com/cplieger/go/wiregen/testdata/basic"
)

// Windows resolves ".." lexically before it opens a path, so an OpenAPI path
// that climbs out of a symlink, a\link\..\types.gen.ts, is a\types.gen.ts. It
// collides with nothing, and the OpenAPI file lands in a, beside the link,
// not on the TS directory's types.gen.ts, where the physical ".." would put it.
func TestGenerate_ClimbingOutOfASymlinkIsLexicalOnWindows(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"a", "sub"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("..", "sub"), filepath.Join(dir, "a", "link")); err != nil {
		t.Fatal(err)
	}
	r := newTestRegistry(wiregen.WithOpenAPIFiles(filepath.Join(dir, "a", "link") + `\..\types.gen.ts`))
	r.Types = []wiregen.Type{wiregen.TypeRef[basic.Address]()}
	r.OpenAPI = wiregen.OpenAPIInfo{Title: "t", Version: "1"}
	if err := r.Generate(t.Context(), append(r.opts, wiregen.WithTSDir(dir))...); err != nil {
		t.Fatalf("Generate = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "a", "types.gen.ts")); err != nil || !strings.HasPrefix(string(got), "{") {
		t.Fatalf(`a\types.gen.ts = %.40q, %v; want the OpenAPI document`, got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "types.gen.ts")); err != nil || strings.HasPrefix(string(got), "{") {
		t.Fatalf("the TS directory's types.gen.ts = %.40q, %v; want the TypeScript output", got, err)
	}
}
