package webbundle

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evanw/esbuild/pkg/api"
)

// An output esbuild's metafile lists but did not hand back cannot be read
// for BUNDLE_URLS, so the check refuses it rather than pass unread.
func TestRefuseUnreadURLsRefusesAnOutputItDoesNotHold(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := built{opts: &api.BuildOptions{AbsWorkingDir: dir, Outdir: filepath.Join(dir, "out")}}
	meta := `{"outputs": {"out/assets/app-X.js": {"entryPoint": "src/app.ts"}}}`
	if err := json.Unmarshal([]byte(meta), &r.meta); err != nil {
		t.Fatal(err)
	}
	err := refuseUnreadURLs(&r, []Entry{{Name: "app", Path: "src/app.ts", Kind: Module}}, nil)
	if err == nil || !strings.Contains(err.Error(), "assets/app-X.js") {
		t.Errorf("refuseUnreadURLs on an output missing from the build = %v, want an error naming assets/app-X.js", err)
	}
}
