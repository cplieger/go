package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/go/webbundle"
)

// newSrc writes a stylesheet that imports another and names a font by url().
func newSrc(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	for name, body := range map[string]string{
		"css/app.css":         "@import \"./base.css\";\n@font-face { font-family: F; src: url(../fonts/f.woff2); }\n",
		"css/base.css":        ".fixture-base { color: rgb(1 2 3); }\n",
		"fonts/f.woff2":       "wOF2 fixture bytes",
		"css/old.css":         "@font-face { font-family: O; src: url(../fonts/o.ttf); }\n",
		"fonts/o.ttf":         "ttf bytes",
		"css/not-a-sheet.txt": "text",
	} {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

func runStyle(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestPrintsBuildStyleText(t *testing.T) {
	t.Parallel()
	src := newSrc(t)
	want, err := webbundle.BuildStyle(webbundle.Stylesheet{Src: src, Path: "css/app.css"})
	if err != nil {
		t.Fatalf("BuildStyle: %v", err)
	}
	code, stdout, stderr := runStyle("-src", src, "css/app.css")
	if code != 0 {
		t.Fatalf("exit %d, want 0; stderr: %s", code, stderr)
	}
	if stdout != string(want) {
		t.Errorf("stdout\n%q\nwant BuildStyle's text\n%q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr on success: %q", stderr)
	}
}

func TestSrcDefaultsToWorkingDirectory(t *testing.T) {
	src := newSrc(t)
	want, err := webbundle.BuildStyle(webbundle.Stylesheet{Src: src, Path: "css/app.css"})
	if err != nil {
		t.Fatalf("BuildStyle: %v", err)
	}
	t.Chdir(src)
	code, stdout, stderr := runStyle("css/app.css")
	if code != 0 {
		t.Fatalf("exit %d, want 0; stderr: %s", code, stderr)
	}
	if stdout != string(want) {
		t.Errorf("stdout\n%q\nwant BuildStyle's text\n%q", stdout, want)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	t.Parallel()
	src := newSrc(t)
	cases := []struct {
		name string
		args []string
	}{
		{name: "NoPath", args: []string{"-src", src}},
		{name: "TwoPaths", args: []string{"-src", src, "css/app.css", "css/base.css"}},
		{name: "UnknownFlag", args: []string{"-out", src, "css/app.css"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, stdout, stderr := runStyle(tc.args...)
			if code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
			if stdout != "" {
				t.Errorf("stdout on a usage error: %q", stdout)
			}
			if !strings.Contains(stderr, "usage: style [-src dir] path") {
				t.Errorf("stderr does not carry the usage line: %q", stderr)
			}
		})
	}
}

func TestHelpExitsZero(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := runStyle("-h")
	if code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	if stdout != "" {
		t.Errorf("stdout on -h: %q", stdout)
	}
	if !strings.Contains(stderr, "usage: style [-src dir] path") {
		t.Errorf("stderr does not carry the usage line: %q", stderr)
	}
}

func TestBuildFailureExitsOneNamingTheFile(t *testing.T) {
	t.Parallel()
	src := newSrc(t)
	cases := []struct{ name, path, names string }{
		{name: "ExtensionOutsideTable", path: "css/old.css", names: "o.ttf"},
		{name: "NotCSS", path: "css/not-a-sheet.txt", names: "css/not-a-sheet.txt"},
		{name: "Missing", path: "css/gone.css", names: "css/gone.css"},
		{name: "OutsideSrc", path: "../app.css", names: "../app.css"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, stdout, stderr := runStyle("-src", src, tc.path)
			if code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			if stdout != "" {
				t.Errorf("stdout on a failed build: %q", stdout)
			}
			if !strings.Contains(stderr, tc.names) {
				t.Errorf("stderr %q does not name %s", stderr, tc.names)
			}
		})
	}
}
