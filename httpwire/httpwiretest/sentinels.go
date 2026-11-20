package httpwiretest

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/httpwire"
	"github.com/cplieger/go/httpwire/internal/wirelink"
)

// Sentinels fails t unless the exported Err* vars that the package in the
// working directory declares (its non-test files for this build, as go test
// sets the directory) are exactly the keys of owner.Sentinels, and owner
// passes every owner check New makes but the catalog's, so a sentinel with
// no code fails the owner package's own test.
func Sentinels(t testing.TB, owner *httpwire.Owner) {
	t.Helper()
	if owner == nil {
		t.Fatal("httpwiretest: Sentinels of a nil owner")
		return
	}
	declared := declaredSentinels(t)
	for _, name := range declared {
		if _, ok := owner.Sentinels[name]; !ok {
			t.Errorf("httpwiretest: owner %q: the package declares %s, which its Sentinels lack", owner.Name, name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(owner.Sentinels)) {
		if !slices.Contains(declared, name) {
			t.Errorf("httpwiretest: owner %q: its Sentinels name %s, which the package does not declare", owner.Name, name)
		}
	}
	for _, err := range wirelink.OwnerProblems(owner) {
		t.Error(err)
	}
}

// declaredSentinels parses the files, since Go cannot list a package's vars
// by reflection.
func declaredSentinels(t testing.TB) []string {
	t.Helper()
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("httpwiretest: Sentinels: reading the package: %v", err)
		return nil
	}
	fset := token.NewFileSet()
	var declared []string
	for _, name := range slices.Concat(pkg.GoFiles, pkg.CgoFiles) {
		file, err := parser.ParseFile(fset, filepath.Join(pkg.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("httpwiretest: Sentinels: %v", err)
			return nil
		}
		declared = append(declared, sentinelsOf(file)...)
	}
	if len(declared) == 0 {
		t.Fatalf("httpwiretest: Sentinels: the package in %s declares no exported Err* var", pkg.Dir)
	}
	slices.Sort(declared)
	return declared
}

// sentinelsOf is file's package-level Err* vars.
func sentinelsOf(file *ast.File) []string {
	var names []string
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
			for _, spec := range gen.Specs {
				names = append(names, errNames(spec)...)
			}
		}
	}
	return names
}

func errNames(spec ast.Spec) []string {
	vs, ok := spec.(*ast.ValueSpec)
	if !ok {
		return nil
	}
	var names []string
	for _, id := range vs.Names {
		if strings.HasPrefix(id.Name, "Err") {
			names = append(names, id.Name)
		}
	}
	return names
}
