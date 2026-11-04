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
	single := entry(yaml.ConstructorStage, "inner", errors.New("inner"))
	tests := map[string]struct {
		err  error
		want bool
	}{
		"app entry":                       {err: &yaml.LoadErrors{Errors: []*yaml.LoadError{entry(yaml.ConstructorStage, app.Error(), app)}}, want: true},
		"no cause":                        {err: &yaml.LoadErrors{Errors: []*yaml.LoadError{entry(yaml.ConstructorStage, "x", nil)}}},
		"non-constructor stage":           {err: &yaml.LoadErrors{Errors: []*yaml.LoadError{entry(yaml.ComposerStage, app.Error(), app)}}},
		"nil entry":                       {err: &yaml.LoadErrors{Errors: []*yaml.LoadError{nil}}},
		"empty LoadErrors":                {err: &yaml.LoadErrors{}},
		"cause wraps a single LoadError":  {err: &yaml.LoadErrors{Errors: []*yaml.LoadError{entry(yaml.ConstructorStage, "w", fmt.Errorf("w: %w", single))}}},
		"cause wraps an empty LoadErrors": {err: &yaml.LoadErrors{Errors: []*yaml.LoadError{entry(yaml.ConstructorStage, "w", fmt.Errorf("w: %w", &yaml.LoadErrors{}))}}},
		"go-yaml v3 hook message":         {err: &yaml.LoadErrors{Errors: []*yaml.LoadError{entry(yaml.ConstructorStage, "T implements UnmarshalYAML(*v3.Node); v4 cannot invoke v3 Node hooks by default", app)}}},
		"single LoadError":                {err: entry(yaml.ConstructorStage, app.Error(), app)},
		"plain error":                     {err: app},
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
