package webbundle

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/go/httpserver/v2"
)

// attr is one attribute of a start tag; start and end are its value's byte
// span in the shell.
type attr struct {
	name       string // lower-case
	value      string
	start, end int
}

type tag struct {
	name  string // lower-case
	attrs []attr
	end   int // the byte after the tag's '>'
}

// rawText are the elements whose content is text, never markup, so a '<'
// inside one starts no tag.
var rawText = []string{"script", "style", "textarea", "title"}

// scanTags calls fn on each start tag in html, comments and the content of
// raw-text elements skipped, as an HTML tokenizer reads them: quoted
// attribute values may hold '>' and names compare case-insensitively.
func scanTags(html []byte, fn func(tag)) {
	for i := 0; i < len(html); {
		rest := html[i:]
		switch {
		case rest[0] != '<' || len(rest) < 2:
			i++
		case bytes.HasPrefix(rest, []byte("<!--")):
			end := bytes.Index(rest[4:], []byte("-->"))
			if end < 0 {
				return
			}
			i += 4 + end + 3
		case !isLetter(rest[1]):
			i++
		default:
			t := readTag(html, i+1)
			fn(t)
			i = t.end
			if slices.Contains(rawText, t.name) {
				end := bytes.Index(bytes.ToLower(html[i:]), []byte("</"+t.name))
				if end < 0 {
					return
				}
				i += end
			}
		}
	}
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

// readTag reads the start tag whose name begins at i.
func readTag(html []byte, i int) tag {
	j := i
	for j < len(html) && !isSpace(html[j]) && html[j] != '/' && html[j] != '>' {
		j++
	}
	t := tag{name: strings.ToLower(string(html[i:j]))}
	for {
		for j < len(html) && (isSpace(html[j]) || html[j] == '/') {
			j++
		}
		if j >= len(html) || html[j] == '>' {
			t.end = min(j+1, len(html))
			return t
		}
		var a attr
		a, j = readAttr(html, j)
		t.attrs = append(t.attrs, a)
	}
}

// readAttr reads the attribute whose name begins at i and returns it and
// the byte after it.
func readAttr(html []byte, i int) (a attr, next int) {
	k := i + 1
	for k < len(html) && !isSpace(html[k]) && html[k] != '/' && html[k] != '>' && html[k] != '=' {
		k++
	}
	a.name = strings.ToLower(string(html[i:k]))
	j := skipSpace(html, k)
	if j >= len(html) || html[j] != '=' {
		return a, j
	}
	a.start, a.end, j = attrValue(html, skipSpace(html, j+1))
	a.value = string(html[a.start:a.end])
	return a, j
}

func skipSpace(html []byte, i int) int {
	for i < len(html) && isSpace(html[i]) {
		i++
	}
	return i
}

// attrValue reads the attribute value at i, quoted or not, and returns its
// span and the byte after it.
func attrValue(html []byte, i int) (start, end, next int) {
	if i < len(html) && (html[i] == '"' || html[i] == '\'') {
		q := bytes.IndexByte(html[i+1:], html[i])
		if q < 0 {
			return i + 1, len(html), len(html)
		}
		return i + 1, i + 1 + q, i + 2 + q
	}
	j := i
	for j < len(html) && !isSpace(html[j]) && html[j] != '>' {
		j++
	}
	return i, j, j
}

// shell is one shell read from Src: its bytes and each entry's logical path
// it names, with the span to replace.
type shell struct {
	name  string
	body  []byte
	links []link
}

type link struct {
	entry      int // index into build.entries
	start, end int
}

// readShell reads a shell and refuses what httpserver.SPA's policy would
// block: an inline script or style element, a style attribute, and an inline
// event handler (an on* attribute, which script-src 'self' blocks as it does
// an inline script). It also refuses a logical path named twice and a
// root-level script or stylesheet path no entry is.
func (b *build) readShell(name string) (shell, error) {
	body, err := os.ReadFile(filepath.Join(b.src, name))
	if err != nil {
		return shell{}, fmt.Errorf("webbundle: shell %s: %w", name, err)
	}
	if bad := httpserver.CheckShell(body); bad != nil {
		return shell{}, fmt.Errorf("webbundle: shell %s %w", name, bad)
	}
	logical := map[string]int{}
	for i, e := range b.entries {
		logical[logicalPath(e)] = i
	}
	sh := shell{name: name, body: body}
	named := map[string]bool{}
	scanTags(body, func(t tag) {
		for _, a := range t.attrs {
			if err == nil {
				err = sh.attr(t, a, logical, named)
			}
		}
	})
	return sh, err
}

// attr checks one attribute of t and records it when it names an entry.
func (s *shell) attr(t tag, a attr, logical map[string]int, named map[string]bool) error {
	if strings.HasPrefix(a.name, "on") {
		return fmt.Errorf("webbundle: shell %s: <%s> carries %s, an inline event handler the policy blocks", s.name, t.name, a.name)
	}
	ref := t.name == "script" && a.name == "src" || t.name == "link" && a.name == "href"
	if !ref || !rootScriptOrStyle(a.value) {
		return nil
	}
	i, ok := logical[a.value]
	switch {
	case !ok:
		return fmt.Errorf("webbundle: shell %s names %s, which no entry is", s.name, a.value)
	case named[a.value]:
		return fmt.Errorf("webbundle: shell %s names %s more than once", s.name, a.value)
	}
	named[a.value] = true
	s.links = append(s.links, link{entry: i, start: a.start, end: a.end})
	return nil
}

// rootScriptOrStyle reports whether v has a logical path's shape:
// "/<name>.js" or "/<name>.css".
func rootScriptOrStyle(v string) bool {
	name, ok := strings.CutPrefix(v, "/")
	return ok && name != "" && !strings.ContainsAny(name, "/?#\\") &&
		(strings.HasSuffix(name, jsExt) || strings.HasSuffix(name, cssExt))
}

// render returns the shell with each logical path replaced by its entry's
// hashed one.
func (s *shell) render(hashed []string) []byte {
	out := make([]byte, 0, len(s.body)+len(s.links)*16)
	at := 0
	for _, l := range s.links {
		out = append(out, s.body[at:l.start]...)
		out = append(out, '/')
		out = append(out, hashed[l.entry]...)
		at = l.end
	}
	return append(out, s.body[at:]...)
}
