//go:build windows

package wiregen

import (
	"os"
	"path/filepath"
	"testing"
)

// A relative OpenAPI path resolves as Windows resolves it: a rooted path on
// the working directory's drive, a drive-relative path against that drive's
// current directory, and ".." lexically.
func TestWorkingDirPath_ResolvesAsWindowsDoes(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	drive := filepath.VolumeName(wd)
	for _, tc := range []struct{ in, want string }{
		{`\api\openapi.json`, drive + `\api\openapi.json`},
		{`/api/openapi.json`, drive + `\api\openapi.json`},
		{drive + `api\openapi.json`, filepath.Join(wd, "api", "openapi.json")},
		{`a\..\openapi.json`, filepath.Join(wd, "openapi.json")},
	} {
		got, err := workingDirPath(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("workingDirPath(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}
