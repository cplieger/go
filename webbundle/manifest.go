package webbundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/go/httpserver/v2"
)

// srcMember is the manifest member naming an image file.
const srcMember = "src"

// appManifest is the app's manifest file, read and checked.
type appManifest struct {
	doc  map[string]any
	path string
	srcs []manifestSrc
}

// manifestSrc is one "src" member and the file under Src it names.
type manifestSrc struct {
	in   map[string]any // the object holding the member
	file string
}

func (b *build) readManifest(name string) (*appManifest, error) {
	p, err := b.srcFile("manifest", name)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(filepath.Join(b.src, filepath.FromSlash(p)))
	if err != nil {
		return nil, fmt.Errorf("webbundle: manifest %s: %w", p, err)
	}
	doc, err := decodeManifest(body)
	if err == nil {
		err = checkManifest(doc)
	}
	if err != nil {
		return nil, fmt.Errorf("webbundle: manifest %s: %w", p, err)
	}
	m := &appManifest{doc: doc, path: p}
	return m, b.findSrcs(m, doc)
}

// decodeManifest reads one JSON object, keeping each number's text.
func decodeManifest(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("it is not JSON: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("it holds more than one JSON value")
	}
	doc, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("it is not a JSON object")
	}
	return doc, nil
}

func checkManifest(doc map[string]any) error {
	if name, _ := doc["name"].(string); strings.TrimSpace(name) == "" {
		return errors.New(`its "name" is missing, empty or not a string`)
	}
	for _, k := range []string{"theme_color", "background_color"} {
		v, ok := doc[k]
		if !ok {
			continue
		}
		if s, _ := v.(string); !hexColour.MatchString(s) {
			return fmt.Errorf("%s is %s, not a #rrggbb colour", k, jsonText(v))
		}
	}
	if _, ok := doc["icons"]; ok {
		return errors.New(`it holds "icons"; Build writes them from Config.Icon and Config.MaskableIcon`)
	}
	return checkManifestURLs(doc)
}

// manifestURL is one URL member Build checks, by its place in the manifest.
type manifestURL struct {
	at, value string
	inScope   bool
}

// checkManifestURLs checks that id, start_url, scope, each shortcut's url
// and share_target.action are root-relative and that all but id lie inside
// the scope.
func checkManifestURLs(doc map[string]any) error {
	var urls []manifestURL
	for _, k := range []string{"id", "start_url", "scope"} {
		v, ok := doc[k]
		if !ok {
			continue
		}
		u := manifestURL{at: k, inScope: k == "start_url"}
		if err := urlString(k, v, &u.value); err != nil {
			return err
		}
		urls = append(urls, u)
	}
	more, err := memberURLs(doc)
	if err != nil {
		return err
	}
	urls = append(urls, more...)
	for _, u := range urls {
		if err := rootRelative(u.value); err != nil {
			return fmt.Errorf("%s %q %w", u.at, u.value, err)
		}
	}
	scope, ok := manifestScope(doc)
	if !ok {
		return nil
	}
	for _, u := range urls {
		if u.inScope && !withinScope(u.value, scope) {
			return fmt.Errorf("%s %q is outside the scope %q", u.at, u.value, scope)
		}
	}
	return nil
}

// memberURLs returns each shortcut's url and share_target.action.
func memberURLs(doc map[string]any) ([]manifestURL, error) {
	urls, err := shortcutURLs(doc)
	if err != nil {
		return nil, err
	}
	v, ok := doc["share_target"]
	if !ok {
		return urls, nil
	}
	s, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("share_target is %s, not an object", jsonText(v))
	}
	u := manifestURL{at: "share_target.action", inScope: true}
	if err := urlString(u.at, s["action"], &u.value); err != nil {
		return nil, err
	}
	return append(urls, u), nil
}

func shortcutURLs(doc map[string]any) ([]manifestURL, error) {
	v, ok := doc["shortcuts"]
	if !ok {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("shortcuts is %s, not an array", jsonText(v))
	}
	urls := make([]manifestURL, 0, len(list))
	for i, item := range list {
		at := fmt.Sprintf("shortcuts[%d]", i)
		s, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s is %s, not an object", at, jsonText(item))
		}
		u := manifestURL{at: at + ".url", inScope: true}
		if err := urlString(u.at, s["url"], &u.value); err != nil {
			return nil, err
		}
		urls = append(urls, u)
	}
	return urls, nil
}

func urlString(at string, v any, dst *string) error {
	if v == nil {
		return fmt.Errorf("%s is missing", at)
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("%s is %s, not a URL string", at, jsonText(v))
	}
	*dst = s
	return nil
}

// rootRelative refuses a URL that is not a path from the origin's root: a
// browser resolves "//host" and "/\host" to another host.
func rootRelative(s string) error {
	if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.ContainsRune(s, '\\') {
		return errors.New(`is not a root-relative URL such as "/app/"`)
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("is no URL: %w", err)
	}
	if dotSegment(u.EscapedPath()) {
		return errors.New("holds a dot segment")
	}
	return nil
}

// manifestScope is the manifest's navigation scope as the manifest spec
// processes it (https://www.w3.org/TR/appmanifest/#scope-member): the
// scope member, else start_url's folder; with neither, it is the folder of
// the page the app is installed from, which Build cannot know.
func manifestScope(doc map[string]any) (string, bool) {
	if s, ok := doc["scope"].(string); ok {
		return urlPath(s), true
	}
	if s, ok := doc["start_url"].(string); ok {
		p := urlPath(s)
		return p[:strings.LastIndex(p, "/")+1], true
	}
	return "", false
}

// withinScope is the manifest spec's "within scope": target's path starts
// with scope's (https://www.w3.org/TR/appmanifest/#dfn-within-scope).
func withinScope(target, scope string) bool {
	return strings.HasPrefix(urlPath(target), scope)
}

// urlPath is a root-relative URL's path, rootRelative having checked it.
func urlPath(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	return u.EscapedPath()
}

// findSrcs records every "src" member in v, at any depth, with the file
// under Src it names by its path from the manifest's folder, as a browser
// resolves it against the manifest's URL.
func (b *build) findSrcs(m *appManifest, v any) error {
	switch v := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(v)) {
			if err := b.findSrc(m, v, k); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range v {
			if err := b.findSrcs(m, item); err != nil {
				return err
			}
		}
	}
	return nil
}

// findSrc records obj[k] when k is "src", else looks inside it.
func (b *build) findSrc(m *appManifest, obj map[string]any, k string) error {
	if k != srcMember {
		return b.findSrcs(m, obj[k])
	}
	file, err := b.srcOf(m.path, obj[k])
	if err != nil {
		return err
	}
	m.srcs = append(m.srcs, manifestSrc{in: obj, file: file})
	return nil
}

func (b *build) srcOf(manifest string, v any) (string, error) {
	s, _ := v.(string)
	u, err := url.Parse(s)
	if s == "" || err != nil || u.Scheme != "" || u.Host != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.HasPrefix(s, "/") || strings.ContainsRune(s, '\\') {
		return "", fmt.Errorf("webbundle: manifest %s: src %s is not a file's path from the manifest's folder", manifest, jsonText(v))
	}
	file, err := b.srcFile("manifest src", path.Join(path.Dir(manifest), u.Path))
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(httpserver.ContentTypes()[path.Ext(file)], "image/") {
		return "", fmt.Errorf("webbundle: manifest %s: src %q is not an image file httpserver serves", manifest, s)
	}
	return file, nil
}

// write writes each src file as a hashed asset named by its served URL,
// adds icons, and writes the manifest as a hashed asset.
func (m *appManifest) write(t *tree, src string, icons []any) (string, error) {
	for _, s := range m.srcs {
		body, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(s.file)))
		if err != nil {
			return "", fmt.Errorf("webbundle: manifest src %s: %w", s.file, err)
		}
		ext := path.Ext(s.file)
		hashed, err := t.writeHashed(strings.TrimSuffix(path.Base(s.file), ext), ext, body)
		if err != nil {
			return "", err
		}
		s.in[srcMember] = "/" + hashed
	}
	m.doc["icons"] = icons
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m.doc); err != nil {
		return "", fmt.Errorf("webbundle: manifest %s: %w", m.path, err)
	}
	return t.writeHashed("manifest", ".webmanifest", body.Bytes())
}

// jsonText is v as JSON, for an error naming a manifest value.
func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
