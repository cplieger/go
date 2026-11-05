package yamlenv

import (
	"errors"
	"fmt"
	"testing"

	"go.yaml.in/yaml/v4"
)

// TestAllDigits pins the guard on the duplicate-key entry's kept line number.
func TestAllDigits(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"1": true, "123": true, "": false, "1 ": false, " 1": false, "1x": false, "-1": false, "١": false,
	}
	for in, want := range tests {
		if got := allDigits(in); got != want {
			t.Errorf("allDigits(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestLocator pins the position prefix built from a Mark's integer fields.
func TestLocator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mark yaml.Mark
		want string
	}{
		{yaml.Mark{Line: 3, Column: 7}, "line 3, column 7: "},
		{yaml.Mark{Line: 3}, "line 3: "},
		{yaml.Mark{Column: 7}, ""},
		{yaml.Mark{Index: 40}, ""},
		{yaml.Mark{Line: -1, Column: 2}, ""},
		{yaml.Mark{Line: 2, Column: -1}, "line 2: "},
	}
	for _, tt := range tests {
		if got := locator(tt.mark); got != tt.want {
			t.Errorf("locator(%+v) = %q, want %q", tt.mark, got, tt.want)
		}
	}
}

// TestClassifyDecodeError pins the per-entry provenance rules on hand-built
// entries, including cause shapes go-yaml's own decode does not produce.
func TestClassifyDecodeError(t *testing.T) {
	t.Parallel()
	app := errors.New("app says no")
	entry := func(stage yaml.Stage, msg string, cause error) *yaml.LoadError {
		return yaml.NewLoadError(stage, msg, yaml.Mark{Line: 1, Column: 1}, cause)
	}
	hookErrors := func(msg string) error {
		return &yaml.LoadErrors{Errors: []*yaml.LoadError{entry(yaml.ConstructorStage, msg, errors.New(msg))}}
	}
	single := entry(yaml.ConstructorStage, "inner", errors.New("inner"))
	type tc struct {
		err  error
		want bool
	}
	tests := map[string]tc{
		"app entry":                       {err: &yaml.LoadErrors{Errors: []*yaml.LoadError{entry(yaml.ConstructorStage, app.Error(), app)}}, want: true},
		"app message mentioning yaml":     {err: hookErrors("invalid yaml value (withheld)"), want: true},
		"no cause":                        {err: &yaml.LoadErrors{Errors: []*yaml.LoadError{entry(yaml.ConstructorStage, "x", nil)}}},
		"non-constructor stage":           {err: &yaml.LoadErrors{Errors: []*yaml.LoadError{entry(yaml.ComposerStage, app.Error(), app)}}},
		"nil entry":                       {err: &yaml.LoadErrors{Errors: []*yaml.LoadError{nil}}},
		"empty LoadErrors":                {err: &yaml.LoadErrors{}},
		"cause wraps a single LoadError":  {err: &yaml.LoadErrors{Errors: []*yaml.LoadError{entry(yaml.ConstructorStage, "w", fmt.Errorf("w: %w", single))}}},
		"cause wraps an empty LoadErrors": {err: &yaml.LoadErrors{Errors: []*yaml.LoadError{entry(yaml.ConstructorStage, "w", fmt.Errorf("w: %w", &yaml.LoadErrors{}))}}},
		"go-yaml v3 hook message":         {err: hookErrors("T implements UnmarshalYAML(*v3.Node); v4 cannot invoke v3 Node hooks by default")},
		"single LoadError":                {err: entry(yaml.ConstructorStage, app.Error(), app)},
		"plain error":                     {err: app},
	}
	// Each is go-yaml's text as a config type's error carries it once copied
	// with %v or errors.New, which drops the *LoadError chain.
	for name, msg := range map[string]string{
		"wrong type after context":        "ctx: cannot construct !!str `x` into int",
		"LoadErrors text":                 "ctx: yaml: construct errors: line 1: cannot construct !!str `x` into int",
		"LoadError text":                  "ctx: go-yaml load error in constructor at L1.C4: cannot construct !!str `x` into int",
		"yaml-prefixed loader message":    "yaml: no documents in stream",
		"resolver message":                "cannot construct !!int `x` as a !!int",
		"unhashable key":                  `cannot use '[]interface {}{"x"}' as a map key; try decoding into yaml.Node`,
		"unknown anchor":                  "unknown anchor 'x' referenced",
		"alias-data message":              `alias-data: invalid node for "x"`,
		"duplicate key after context":     `ctx: mapping key "a" already defined at line 1`,
		"unknown field after context":     "ctx: field a not found in type T",
		"field already set after context": "ctx: field a already set in type T",
	} {
		tests["copied go-yaml message: "+name] = tc{err: hookErrors(msg)}
	}
	for name, tt := range tests {
		got := classifyDecodeError(tt.err)
		_, wrapped := got.(*UnmarshalerError)
		if wrapped != tt.want {
			t.Errorf("%s: classifyDecodeError = %T, want wrapped=%v", name, got, tt.want)
		}
		if !wrapped && got != tt.err {
			t.Errorf("%s: classifyDecodeError changed an unclaimed error: %v", name, got)
		}
	}
}
