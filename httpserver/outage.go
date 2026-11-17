package httpserver

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/cplieger/go/textsafe"
)

// OutageConfig configures OutagePage.
type OutageConfig struct {
	Assets    fs.FS           // the bundle; its style entry styles the page when built
	Errors    ErrorResponder  // answers under APIPrefix and for a refused method; nil is WriteError
	Style     string          // the style entry's logical name ("app.css"); "" is unstyled
	Title     string          // required: "seadex-scout cannot start"
	App       string          // required: the app's name, written into the class sentences
	APIPrefix string          // the API's path prefix ("/api"); the page answers nothing at or under it
	Notice    string          // one line drawn above the commands; "" draws none
	Files     []OutageFile    // each file that cannot open
	Commands  []OutageCommand // the commands that recover the files, in order
	Restart   bool            // the closing line names a container restart instead of a reload
}

// OutageFile is one file that cannot open. The page writes Name and the
// class's sentence; the error that classed it belongs in the log.
type OutageFile struct {
	Name  string      // required: the file's setting and path as the person knows it ("ACCOUNTS_PATH, /config/accounts.db")
	Class OutageClass // why it cannot open
}

// OutageClass is why a file cannot open, from a closed set: OutagePage
// refuses any value but the six constants.
type OutageClass string

// The outage classes, each with the one sentence the page writes for it.
const (
	OutageDamaged    OutageClass = "damaged"    // "The file is damaged."
	OutageFormat     OutageClass = "format"     // "The file was written by another version of <app>, or is not <app>'s file."
	OutageNewer      OutageClass = "newer"      // "The file was written by a newer version of <app>."
	OutageUnreadable OutageClass = "unreadable" // "The file cannot be read."
	OutageKey        OutageClass = "key"        // "The key file is missing or does not match this accounts file."
	OutageUnwritable OutageClass = "unwritable" // "The file cannot be written. Free space on its disk."
)

// OutageCommand is one command line that recovers the files, as run in the
// container, and the note the page draws under it.
type OutageCommand struct {
	Run  string // required: "docker exec seadex-scout seadex-scout accounts restore <archive>"
	Note string // "" draws none
}

// outageSentence is each class's sentence, OutageConfig.App written in place
// of <app>.
var outageSentence = map[OutageClass]string{
	OutageDamaged:    "The file is damaged.",
	OutageFormat:     "The file was written by another version of <app>, or is not <app>'s file.",
	OutageNewer:      "The file was written by a newer version of <app>.",
	OutageUnreadable: "The file cannot be read.",
	OutageKey:        "The key file is missing or does not match this accounts file.",
	OutageUnwritable: "The file cannot be written. Free space on its disk.",
}

const (
	outageReload  = "Reload this page once the command finishes."
	outageRestart = "Run the command above or restart the container, then reload this page."
	// maxOutageLine bounds each line the page draws from OutageConfig, in bytes.
	maxOutageLine = 512
)

var outageTemplate = template.Must(template.New("outage").Parse(`<!doctype html>
<html class="cp" lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
{{- with .Style}}
<link rel="stylesheet" href="{{.}}">
{{- end}}
</head>
<body>
<main class="cp-gate">
<section class="cp-panel cp-stack">
<h1>{{.Title}}</h1>
{{- range .Files}}
<p><code class="cp-mono">{{.Name}}</code><br>{{.Sentence}}</p>
{{- end}}
{{- with .Notice}}
<p>{{.}}</p>
{{- end}}
{{- if .Commands}}
<ul class="cp-stack">
{{- range .Commands}}
<li><code class="cp-mono">{{.Run}}</code>{{with .Note}}<br>{{.}}{{end}}</li>
{{- end}}
</ul>
{{- end}}
<p>{{.Closing}}</p>
</section>
</main>
</body>
</html>
`))

type outageFileView struct{ Name, Sentence string }

type outageView struct {
	Title, Style, Notice, Closing string
	Files                         []outageFileView
	Commands                      []OutageCommand
}

// OutagePage serves the static page an app shows while a file it needs
// cannot open: every GET or HEAD outside APIPrefix answers 503, no-store,
// under SPAPolicy's default policy, with the title, each file's Name and class
// sentence, the Notice, each command's Run and Note, and the closing line; the
// four named fields are cleaned through textsafe. A file the bundle lists under
// assets/ answers as SPA serves it; a path at or under APIPrefix answers
// not_found and another method method_not_allowed, through Errors, no-store.
// The error names the refused field (a blank Title or App too), class or bundle.
func OutagePage(c OutageConfig) (http.Handler, error) { //nolint:gocritic // hugeParam: read once at construction, and a value keeps the caller's config unshared
	if strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.App) == "" {
		return nil, errors.New("httpserver: an outage page needs a Title and an App")
	}
	if err := checkAPIPrefix(c.APIPrefix); err != nil {
		return nil, err
	}
	v, err := newOutageView(c)
	if err != nil {
		return nil, err
	}
	policy, err := SPAPolicy(nil)
	if err != nil {
		return nil, err
	}
	p := &outagePage{policy: policy, apiPrefix: c.APIPrefix, errs: c.Errors}
	if p.errs == nil {
		p.errs = WriteError
	}
	b, built, err := ReadBundle(c.Assets)
	if err != nil {
		return nil, err
	}
	if built {
		if p.assets, p.served, err = bundleAssets(c.Assets, b, policy); err != nil {
			return nil, err
		}
		if v.Style, err = outageStyle(b, c.Style); err != nil {
			return nil, err
		}
	}
	var page bytes.Buffer
	if err = outageTemplate.Execute(&page, v); err != nil {
		return nil, fmt.Errorf("httpserver: render the outage page: %w", err)
	}
	p.page = page.Bytes()
	return p, nil
}

// newOutageView is the page's text for c, each line cleaned.
func newOutageView(c OutageConfig) (outageView, error) { //nolint:gocritic // hugeParam: OutagePage's config, read once
	v := outageView{Title: c.Title, Notice: outageLine(c.Notice), Closing: outageReload}
	if c.Restart {
		v.Closing = outageRestart
	}
	for i, f := range c.Files {
		name := outageLine(f.Name)
		if name == "" {
			return outageView{}, fmt.Errorf("httpserver: outage file %d has no Name", i+1)
		}
		sentence, ok := outageSentence[f.Class]
		if !ok {
			return outageView{}, fmt.Errorf("httpserver: outage file %q has class %q; want damaged, format, newer, unreadable, key or unwritable",
				name, textsafe.SanitizeSingleLineBounded(string(f.Class), maxErrorValue))
		}
		v.Files = append(v.Files, outageFileView{Name: name, Sentence: strings.ReplaceAll(sentence, "<app>", c.App)})
	}
	for i, cmd := range c.Commands {
		run := outageLine(cmd.Run)
		if run == "" {
			return outageView{}, fmt.Errorf("httpserver: outage command %d has no Run", i+1)
		}
		v.Commands = append(v.Commands, OutageCommand{Run: run, Note: outageLine(cmd.Note)})
	}
	return v, nil
}

// outageLine is s as the page draws it: one line through textsafe, cut at
// maxOutageLine bytes, and "" when nothing but spaces is left.
func outageLine(s string) string {
	clean := textsafe.SanitizeSingleLineBounded(s, maxOutageLine)
	if strings.TrimSpace(clean) == "" {
		return ""
	}
	return clean
}

// outageStyle is the path the page links for the style entry named style
// ("app.css" is the entry "app" of kind style), or "" when style is empty.
func outageStyle(b Bundle, style string) (string, error) {
	if style == "" {
		return "", nil
	}
	if name, ok := strings.CutSuffix(style, ".css"); ok {
		for _, e := range b.Entries {
			if e.Kind == BundleStyle && e.Name == name {
				return "/" + e.Path, nil
			}
		}
	}
	return "", fmt.Errorf("httpserver: %s builds no style entry %q for the outage page", BundlePath, style)
}

type outagePage struct {
	errs      ErrorResponder
	assets    http.Handler
	served    map[string]bool // tree paths answered through assets
	policy    string
	apiPrefix string
	page      []byte
}

func (p *outagePage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", p.policy)
	if underPrefix(r.URL.Path, p.apiPrefix) || underPrefix(path.Clean(r.URL.Path), p.apiPrefix) {
		refuse(p.errs, w, r, http.StatusNotFound, "not_found", "not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		refuse(p.errs, w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if p.served[strings.TrimPrefix(r.URL.Path, "/")] {
		p.assets.ServeHTTP(w, r)
		return
	}
	h.Set("Content-Type", contentTypes[htmlExt])
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(p.page)))
	w.WriteHeader(http.StatusServiceUnavailable)
	writeBody(w, r, p.page)
}
