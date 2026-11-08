package yamlenv

import (
	"errors"
	"fmt"
	"runtime/debug"
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
		"alias cycle":                     "anchor 'x' value contains itself",
		"tag character":                   "found character '{' that is not allowed in a YAML tag",
		"leading UTF-8 octet":             "invalid leading UTF-8 octet (value: 255)",
		"trailing UTF-8 octet":            "invalid trailing UTF-8 octet (value: 65)",
		"invalid Unicode character":       "invalid Unicode character (value: 65534)",
		"unexpected low surrogate":        "unexpected low surrogate area (value: 56320)",
		"expected low surrogate":          "expected low surrogate area (value: 65)",
		"control character":               "control characters are not allowed (value: 1)",
		"input error":                     "input error: read failed",
		"array length":                    "invalid array: want 2 elements but got 3",
		"merge-key conflict":              `merge-key: conflicting key "x"`,
		"composer event":                  "expected MAPPING_START event but got SCALAR",
	} {
		tests["copied go-yaml message: "+name] = tc{err: hookErrors(msg)}
	}
	for name, tt := range tests {
		if got := classifyDecodeError(tt.err); (got != nil) != tt.want {
			t.Errorf("%s: classifyDecodeError = %v, want owned=%v", name, got, tt.want)
		}
	}
}

// goYAMLModule at auditedGoYAML is the release whose value-quoting messages
// goYAMLMarkers was checked against.
const (
	goYAMLModule  = "go.yaml.in/yaml/v4"
	auditedGoYAML = "v4.0.0"
)

// goYAMLAuditDrift returns why deps do not build goYAMLModule at
// auditedGoYAML, or "" when they do.
func goYAMLAuditDrift(deps []*debug.Module) string {
	for _, dep := range deps {
		if dep.Path != goYAMLModule {
			continue
		}
		switch {
		case dep.Replace != nil:
			return fmt.Sprintf("%s is replaced by %s %s", goYAMLModule, dep.Replace.Path, dep.Replace.Version)
		case dep.Version != auditedGoYAML:
			return fmt.Sprintf("yamlenv builds against %s %s", goYAMLModule, dep.Version)
		}
		return ""
	}
	return goYAMLModule + " is not in the build information"
}

// TestGoYAMLMarkersAudited fails when yamlenv builds against a go-yaml other
// than the one goYAMLMarkers was audited on: a value-quoting message that
// release adds would pass isGoYAMLMessage and reach a passthrough predicate.
func TestGoYAMLMarkersAudited(t *testing.T) {
	t.Parallel()
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("debug.ReadBuildInfo: the test binary has no build information")
	}
	if drift := goYAMLAuditDrift(bi.Deps); drift != "" {
		t.Fatalf("%s, but goYAMLMarkers was audited on %s. Redo the audit: walk every fmt.Sprintf, "+
			"fmt.Errorf and errors.New in the new release's root package, internal/libyaml, internal/plugin "+
			"and plugin that formats document input, add a goYAMLMarkers entry and a TestClassifyDecodeError "+
			"row for each message that quotes it, then set auditedGoYAML to the new version in the same change.",
			drift, auditedGoYAML)
	}
}

// TestGoYAMLAuditDrift pins the tripwire's comparison on synthetic build
// information, so a bump, a replace or a missing entry cannot pass.
func TestGoYAMLAuditDrift(t *testing.T) {
	t.Parallel()
	other := &debug.Module{Path: "example.com/other", Version: auditedGoYAML}
	tests := map[string]struct {
		deps []*debug.Module
		ok   bool
	}{
		"audited":     {deps: []*debug.Module{other, {Path: goYAMLModule, Version: auditedGoYAML}}, ok: true},
		"patch bump":  {deps: []*debug.Module{{Path: goYAMLModule, Version: "v4.0.1"}}},
		"pre-release": {deps: []*debug.Module{{Path: goYAMLModule, Version: auditedGoYAML + "-rc.1"}}},
		"replaced": {deps: []*debug.Module{
			{Path: goYAMLModule, Version: auditedGoYAML, Replace: &debug.Module{Path: "../go-yaml"}},
		}},
		"absent": {deps: []*debug.Module{other}},
	}
	for name, tt := range tests {
		if drift := goYAMLAuditDrift(tt.deps); (drift == "") != tt.ok {
			t.Errorf("%s: goYAMLAuditDrift = %q, want ok=%v", name, drift, tt.ok)
		}
	}
}
