// Package httpservertest holds the test helpers for code built on httpserver.
// It sits apart from httpserver so that package imports no testing.
package httpservertest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
)

const (
	httpserverPath    = "github.com/cplieger/go/httpserver/v2"
	errorCodeType     = "ErrorCode"
	errorResponseType = "ErrorResponse"
)

// rootWriters are the httpserver functions that take an ErrorCode, with its
// argument positions.
var rootWriters = map[string][]int{"WriteError": {3}, "NewErrorResponse": {1}}

// CheckCodeSpecs fails t unless specs holds exactly the codes the non-test Go
// files in dir write, naming each missing from specs and each key nothing
// writes. A code is written when a string literal, a string constant or an
// ErrorCode conversion of one is an ErrorCode argument of a call (WriteError,
// NewErrorResponse, an ErrorResponder, or a function or func type dir declares)
// or fills an ErrorCode field of a composite literal, including
// ErrorResponse.Code. Build envelopes with NewErrorResponse; a literal's code
// is still counted. A code in a variable is counted where its literal enters.
func CheckCodeSpecs(t testing.TB, dir string, specs map[httpserver.ErrorCode]httpserver.CodeSpec) {
	t.Helper()
	written, err := writtenCodes(dir)
	if err != nil {
		t.Fatalf("CheckCodeSpecs: %v", err)
	}
	for _, code := range slices.Sorted(maps.Keys(written)) {
		if _, ok := specs[code]; !ok {
			t.Errorf("%s writes code %q, which CodeSpecs does not hold", written[code], code)
		}
	}
	for _, code := range slices.Sorted(maps.Keys(specs)) {
		if _, ok := written[code]; !ok {
			t.Errorf("CodeSpecs holds %q, which no writer in %s answers", code, dir)
		}
	}
}

// codeScan holds what the declarations in one directory say about where an
// ErrorCode goes, and the codes found so far.
type codeScan struct {
	fset      *token.FileSet
	written   map[httpserver.ErrorCode]string
	consts    map[string]string
	funcs     map[string][]int // function, method or func-typed value name: its ErrorCode argument positions
	funcTypes map[string][]int // func type name: its ErrorCode parameter positions
	fields    map[string]bool  // ErrorCode-typed struct field names
}

// writtenCodes parses the non-test Go files in dir and returns each code they
// write with the position of its first occurrence.
func writtenCodes(dir string) (map[httpserver.ErrorCode]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	s := &codeScan{
		fset:      token.NewFileSet(),
		written:   make(map[httpserver.ErrorCode]string),
		consts:    make(map[string]string),
		funcs:     make(map[string][]int),
		funcTypes: map[string][]int{"ErrorResponder": {3}},
		fields:    make(map[string]bool),
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(s.fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no non-test Go files in %s", dir)
	}
	// Func types first, since a field or parameter of one is a writer.
	for _, f := range files {
		s.declareFuncTypes(f)
	}
	for _, f := range files {
		s.declare(f)
	}
	for _, f := range files {
		s.collect(f)
	}
	return s.written, nil
}

// httpserverName is the name f refers to httpserver by, "" when f is in
// package httpserver itself and the qualifier is absent.
func httpserverName(f *ast.File) (string, bool) {
	if f.Name.Name == "httpserver" {
		return "", true
	}
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == httpserverPath {
			if imp.Name != nil {
				return imp.Name.Name, true
			}
			return "httpserver", true
		}
	}
	return "", false
}

// typeName returns the name of an httpserver type expression (ErrorCode inside
// the package, httpserver.ErrorCode outside it), or a local type's name.
func typeName(f *ast.File, e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			if q, ok := httpserverName(f); ok && x.Name == q {
				return e.Sel.Name
			}
		}
	}
	return ""
}

// codePositions returns the argument positions of ft whose type is ErrorCode.
func codePositions(f *ast.File, ft *ast.FuncType) []int {
	var pos []int
	i := 0
	for _, p := range ft.Params.List {
		n := max(len(p.Names), 1)
		if typeName(f, p.Type) == errorCodeType {
			for k := range n {
				pos = append(pos, i+k)
			}
		}
		i += n
	}
	return pos
}

func (s *codeScan) declareFuncTypes(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		if ts, ok := n.(*ast.TypeSpec); ok {
			if ft, ok := ts.Type.(*ast.FuncType); ok {
				if pos := codePositions(f, ft); len(pos) > 0 {
					s.funcTypes[ts.Name.Name] = pos
				}
			}
		}
		return true
	})
}

// declare records the constants, the writer functions and values, and the
// ErrorCode fields f declares.
func (s *codeScan) declare(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if pos := codePositions(f, n.Type); len(pos) > 0 {
				s.funcs[n.Name.Name] = pos
			}
		case *ast.ValueSpec:
			s.declareValues(f, n)
		case *ast.Field:
			s.declareField(f, n)
		}
		return true
	})
}

func (s *codeScan) declareValues(f *ast.File, v *ast.ValueSpec) {
	for i, name := range v.Names {
		if i < len(v.Values) {
			if lit, ok := stringLit(v.Values[i]); ok {
				s.consts[name.Name] = lit
			}
		}
		if v.Type != nil {
			if pos, ok := s.funcTypes[typeName(f, v.Type)]; ok {
				s.funcs[name.Name] = pos
			}
		}
	}
}

func (s *codeScan) declareField(f *ast.File, fd *ast.Field) {
	tn := typeName(f, fd.Type)
	for _, name := range fd.Names {
		if tn == errorCodeType {
			s.fields[name.Name] = true
		}
		if pos, ok := s.funcTypes[tn]; ok {
			s.funcs[name.Name] = pos
		}
	}
}

// collect records every code f writes.
func (s *codeScan) collect(f *ast.File) {
	q, imported := httpserverName(f)
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			for _, i := range s.callPositions(n, q, imported) {
				if i < len(n.Args) {
					s.record(f, n.Args[i])
				}
			}
		case *ast.CompositeLit:
			s.collectFields(f, n)
		}
		return true
	})
}

// collectFields records the codes lit gives its ErrorCode-typed fields.
func (s *codeScan) collectFields(f *ast.File, lit *ast.CompositeLit) {
	_, qualified := lit.Type.(*ast.SelectorExpr)
	envelope := qualified && typeName(f, lit.Type) == errorResponseType
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && (s.fields[key.Name] || envelope && key.Name == "Code") {
			s.record(f, kv.Value)
		}
	}
}

// callPositions returns the ErrorCode argument positions of call.
func (s *codeScan) callPositions(call *ast.CallExpr, q string, imported bool) []int {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return s.funcs[fn.Name]
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok && imported && q != "" && x.Name == q {
			return rootWriters[fn.Sel.Name]
		}
		return s.funcs[fn.Sel.Name]
	}
	return nil
}

// record adds the code e names, when e is a literal, a constant or an
// ErrorCode conversion of one.
func (s *codeScan) record(f *ast.File, e ast.Expr) {
	code, ok := s.resolve(f, e)
	if !ok {
		return
	}
	c := httpserver.ErrorCode(code)
	if _, seen := s.written[c]; !seen {
		s.written[c] = s.fset.Position(e.Pos()).String()
	}
}

func (s *codeScan) resolve(f *ast.File, e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		return stringLit(e)
	case *ast.Ident:
		v, ok := s.consts[e.Name]
		return v, ok
	case *ast.CallExpr:
		if len(e.Args) == 1 && typeName(f, e.Fun) == errorCodeType {
			return s.resolve(f, e.Args[0])
		}
	case *ast.ParenExpr:
		return s.resolve(f, e.X)
	}
	return "", false
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	return v, err == nil
}
