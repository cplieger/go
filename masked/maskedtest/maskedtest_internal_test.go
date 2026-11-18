package maskedtest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io"
	"log/slog"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// pkg is this package's import path, which names its types in a reported
// line.
const pkg = "github.com/cplieger/go/masked/maskedtest."

// masker stands in for a masked type: Unmasked must not enter it.
type masker struct{ text string }

type words []string

type node struct {
	next  *node
	label string
}

type list []list

type kinds struct {
	str      string
	bytes    []byte
	schedule [60]uint32
	ids      []int64
	boxed    any
	byKey    map[string]int
	byValue  map[int][]uint8
	nested   *struct{ deep []*struct{ s string } }
	masked   masker
	maskedP  *masker
	when     time.Time
	n        int
	ok       bool
	f        func() string
	c        chan string
	p        unsafe.Pointer
	floats   []float64
	reviewed string
}

func TestUnmasked(t *testing.T) {
	t.Parallel()
	allow := Allow{
		Masked: []reflect.Type{reflect.TypeFor[masker]()},
		Fields: []Field{{In: reflect.TypeFor[kinds](), Name: "reviewed"}},
		Types:  []reflect.Type{reflect.TypeFor[time.Time]()},
	}
	t.Run("kinds", func(t *testing.T) {
		t.Parallel()
		// A field this literal does not write is dead to deadset, which keeps a
		// field reflection reads only where the program writes it
		// (github.com/cplieger/deadset-go docs/encoding-reflection.md).
		fixture := &kinds{
			str:      "s",
			bytes:    []byte("b"),
			schedule: [60]uint32{1},
			ids:      []int64{1},
			boxed:    1,
			byKey:    map[string]int{"k": 1},
			byValue:  map[int][]uint8{1: {1}},
			nested:   &struct{ deep []*struct{ s string } }{deep: []*struct{ s string }{{s: "s"}}},
			masked:   masker{text: "t"},
			maskedP:  &masker{text: "t"},
			when:     time.Unix(1, 0),
			n:        1,
			ok:       true,
			f:        func() string { return "" },
			c:        make(chan string),
			p:        unsafe.Pointer(nil),
			floats:   []float64{1},
			reviewed: "r",
		}
		got := Unmasked(allow, reflect.TypeOf(fixture))
		want := []string{
			pkg + "kinds.boxed: holds an interface (interface {})",
			pkg + "kinds.byKey: holds a string (string)",
			pkg + "kinds.byValue: holds an integer sequence ([]uint8)",
			pkg + "kinds.bytes: holds an integer sequence ([]uint8)",
			pkg + "kinds.ids: holds an integer sequence ([]int64)",
			pkg + "kinds.schedule: holds an integer sequence ([60]uint32)",
			pkg + "kinds.str: holds a string (string)",
			"struct { " + pkg + "s string }.s: holds a string (string)",
		}
		if !slices.Equal(got, want) {
			t.Errorf("Unmasked() =\n%q\nwant\n%q", got, want)
		}
	})
	t.Run("root_leaf", func(t *testing.T) {
		t.Parallel()
		got := Unmasked(Allow{}, nil, reflect.TypeFor[map[string]bool]())
		want := []string{"map[string]bool: holds a string (string)"}
		if !slices.Equal(got, want) {
			t.Errorf("Unmasked() = %q, want %q", got, want)
		}
	})
	t.Run("same_type_fields", func(t *testing.T) {
		t.Parallel()
		got := Unmasked(Allow{}, reflect.TypeFor[struct{ a, b words }]())
		want := []string{
			"struct { " + pkg + "a " + pkg + "words; " + pkg + "b " + pkg + "words }.a: holds a string (string)",
			"struct { " + pkg + "a " + pkg + "words; " + pkg + "b " + pkg + "words }.b: holds a string (string)",
		}
		if !slices.Equal(got, want) {
			t.Errorf("Unmasked() = %q, want %q", got, want)
		}
	})
	t.Run("embedded_unexported", func(t *testing.T) {
		t.Parallel()
		// An embedded field's type is written alone, as reflect writes it.
		root := reflect.TypeFor[struct{ words }]()
		got := Unmasked(Allow{}, root)
		if want := []string{"struct { " + pkg + "words }.words: holds a string (string)"}; !slices.Equal(got, want) {
			t.Errorf("Unmasked() = %q, want %q", got, want)
		}
		if got := Unmasked(Allow{Fields: []Field{{In: root, Name: "words"}}}, root); len(got) != 0 {
			t.Errorf("Unmasked(allowing words) = %q, want none", got)
		}
	})
	t.Run("stale_allow", func(t *testing.T) {
		t.Parallel()
		got := Unmasked(allow, reflect.TypeFor[struct{ n int }]())
		want := []string{
			"allow entry " + pkg + "kinds.reviewed: reached by no root",
			"allow entry " + pkg + "masker: reached by no root",
			"allow entry time.Time: reached by no root",
		}
		if !slices.Equal(got, want) {
			t.Errorf("Unmasked() = %q, want %q", got, want)
		}
	})
	t.Run("nil_allow_entries", func(t *testing.T) {
		t.Parallel()
		got := Unmasked(Allow{Masked: []reflect.Type{nil}, Fields: []Field{{Name: "Gone"}}}, reflect.TypeFor[struct{ n int }]())
		want := []string{"allow entry <nil>.Gone: reached by no root", "allow entry <nil>: reached by no root"}
		if !slices.Equal(got, want) {
			t.Errorf("Unmasked() = %q, want %q", got, want)
		}
	})
	t.Run("cycles", func(t *testing.T) {
		t.Parallel()
		loop := &node{label: "loop"}
		loop.next = loop
		done := make(chan []string, 1)
		go func() { done <- Unmasked(Allow{}, reflect.TypeOf(*loop), reflect.TypeFor[list]()) }()
		select {
		case got := <-done:
			want := []string{pkg + "node.label: holds a string (string)"}
			if !slices.Equal(got, want) {
				t.Errorf("Unmasked() = %q, want %q", got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Unmasked did not return within 10s on a cyclic type")
		}
	})
}

func TestLeak(t *testing.T) {
	t.Parallel()
	const text = "s3cret>?~"
	std := func(prefix, suffix string) string {
		return base64.StdEncoding.EncodeToString([]byte(prefix + text + suffix))
	}
	cases := []struct {
		name, got string
		want      bool
	}{
		{name: "clear", got: "x=s3cret>?~;", want: true},
		{name: "hex", got: "7333637265743e3f7e", want: true},
		{name: "upper_hex", got: "7333637265743E3F7E", want: true},
		{name: "std_base64", got: "czNjcmV0Pj9+", want: true},
		{name: "url_base64", got: "czNjcmV0Pj9-", want: true},
		{name: "base64_offset_1", got: std("x", ""), want: true},
		{name: "base64_offset_2", got: std("xy", ""), want: true},
		{name: "base64_offset_1_followed", got: std("x", "!"), want: true},
		{name: "url_base64_offset_2_followed", got: base64.URLEncoding.EncodeToString([]byte("xy" + text + "tail")), want: true},
		{name: "other_text", got: base64.StdEncoding.EncodeToString([]byte("x" + "s3cret>?!")), want: false},
		{name: "redacted", got: "[redacted]", want: false},
	}
	for _, tc := range cases {
		if got := Leak(tc.got, text); got != tc.want {
			t.Errorf("%s: Leak(%q) = %t, want %t", tc.name, tc.got, got, tc.want)
		}
	}
	t.Run("json_string", func(t *testing.T) {
		t.Parallel()
		// encoding/json escapes " and \ and, unless HTML escaping is off as
		// in log/slog's JSON handler, writes <, > and & as \u003c, \u003e and
		// \u0026.
		const escaped = `pa"ss<wo>rd&\`
		for _, tc := range []struct {
			name, got string
			want      bool
		}{
			{name: "marshal", got: `{"password":"pa\"ss\u003cwo\u003erd\u0026\\"}`, want: true},
			{name: "no_html_escape", got: `{"password":"pa\"ss<wo>rd&\\"}`, want: true},
			{name: "inside_a_longer_string", got: `{"msg":"login pa\"ss\u003cwo\u003erd\u0026\\ failed"}`, want: true},
			{name: "other_text", got: `{"password":"pa\"ss\u003cwo\u003erd\u0026/"}`, want: false},
		} {
			if got := Leak(tc.got, escaped); got != tc.want {
				t.Errorf("%s: Leak(%q) = %t, want %t", tc.name, tc.got, got, tc.want)
			}
		}
		// encoding/json writes a control byte as \u0001 and U+2028 as \u2028
		// whether HTML escaping is on or off.
		if got := `{"msg":"ctl\u0001-ls\u2028"}`; !Leak(got, "ctl\x01-ls\u2028") {
			t.Errorf("control_and_line_separator: Leak(%q) = false, want true", got)
		}
	})
	t.Run("encoders", func(t *testing.T) {
		t.Parallel()
		// Each character is escaped by some encoder below: +, /, = and & by
		// net/url, a space, : and ! by URL.String, < and & by html, " and \ by
		// strconv and encoding/json, and the control byte, U+2028 and é by
		// strconv. other differs from secret in the middle.
		const secret, other = "ab+cd/ef=gh&ij<kl mn:op!q\"r\\s\x01\u2028é", "ab+cd/EF=gh&ij<kl mn:op!q\"r\\s\x01\u2028é"
		printf := func(verb string) func(string) string {
			return func(s string) string { return fmt.Sprintf(verb, s) }
		}
		printfBytes := func(verb string) func(string) string {
			return func(s string) string { return fmt.Sprintf(verb, []byte(s)) }
		}
		jsonOut := func(escapeHTML bool) func(string) string {
			return func(s string) string {
				var b strings.Builder
				enc := json.NewEncoder(&b)
				enc.SetEscapeHTML(escapeHTML)
				if err := enc.Encode(map[string]string{"v": s}); err != nil {
					t.Fatalf("Encode() error = %v", err)
				}
				return b.String()
			}
		}
		slogOut := func(handler func(io.Writer) slog.Handler) func(string) string {
			return func(s string) string {
				var b strings.Builder
				slog.New(handler(&b)).Info("login", "v", s)
				return b.String()
			}
		}
		b64 := func(enc *base64.Encoding) func(string) string {
			return func(s string) string { return enc.EncodeToString([]byte(s)) }
		}
		urlError := func(u *url.URL) string {
			return (&url.Error{Op: "Get", URL: u.String(), Err: io.ErrUnexpectedEOF}).Error()
		}
		page := func(src string) func(string) string {
			tmpl := template.Must(template.New("page").Parse(src))
			return func(s string) string {
				var b strings.Builder
				if err := tmpl.Execute(&b, s); err != nil {
					t.Fatalf("Execute() error = %v", err)
				}
				return b.String()
			}
		}
		encoders := map[string]func(string) string{
			"%s": printf("%s"), "%v": printf("%v"), "%q": printf("%q"), "%+q": printf("%+q"),
			"%#q": printf("%#q"), "%#v": printf("%#v"), "%x": printf("%x"), "%X": printf("%X"),
			"bytes %v": printfBytes("%v"), "bytes %d": printfBytes("%d"), "bytes %#v": printfBytes("%#v"),
			"bytes %x": printfBytes("%x"), "bytes %X": printfBytes("%X"),
			"field %v":               func(s string) string { return fmt.Sprintf("%v", struct{ b []byte }{[]byte(s)}) },
			"field %#v":              func(s string) string { return fmt.Sprintf("%#v", struct{ b []byte }{[]byte(s)}) },
			"strconv.Quote":          strconv.Quote,
			"strconv.QuoteToASCII":   strconv.QuoteToASCII,
			"strconv.QuoteToGraphic": strconv.QuoteToGraphic,
			"json":                   jsonOut(true),
			"json_no_html_escape":    jsonOut(false),
			"slog_text":              slogOut(func(w io.Writer) slog.Handler { return slog.NewTextHandler(w, nil) }),
			"slog_json":              slogOut(func(w io.Writer) slog.Handler { return slog.NewJSONHandler(w, nil) }),
			"hex":                    func(s string) string { return hex.EncodeToString([]byte(s)) },
			"upper_hex":              func(s string) string { return strings.ToUpper(hex.EncodeToString([]byte(s))) },
			"base64_std":             b64(base64.StdEncoding),
			"base64_url":             b64(base64.URLEncoding),
			"base64_raw_std":         b64(base64.RawStdEncoding),
			"base64_raw_url":         b64(base64.RawURLEncoding),
			"url.QueryEscape":        url.QueryEscape,
			"url.PathEscape":         url.PathEscape,
			"url.User":               func(s string) string { return url.User(s).String() },
			"url.UserPassword":       func(s string) string { return url.UserPassword("user", s).String() },
			"url.EscapedFragment":    func(s string) string { return (&url.URL{Fragment: s}).EscapedFragment() },
			"url.Error_query": func(s string) string {
				return urlError(&url.URL{Scheme: "https", Host: "example.com", Path: "/api", RawQuery: url.Values{"apikey": {s}}.Encode()})
			},
			"url.Error_path": func(s string) string {
				return urlError(&url.URL{Scheme: "https", Host: "example.com", Path: "/feed/" + s})
			},
			"url.Error_user": func(s string) string {
				return urlError(&url.URL{Scheme: "https", User: url.User(s), Host: "example.com", Path: "/"})
			},
			"url.Error_fragment": func(s string) string {
				return urlError(&url.URL{Scheme: "https", Host: "example.com", Path: "/", Fragment: s})
			},
			"html.EscapeString":          html.EscapeString,
			"html/template_text":         page("<p>{{.}}</p>"),
			"html/template_href":         page(`<a href="{{.}}">feed</a>`),
			"html/template_href_path":    page(`<a href="/feed/{{.}}">feed</a>`),
			"html/template_href_query":   page(`<a href="/api?apikey={{.}}">feed</a>`),
			"html/template_src_fragment": page(`<img src="/logo.png#{{.}}">`),
		}
		for name, render := range encoders {
			for _, s := range []string{secret, "login " + secret + " failed"} {
				if got := render(s); !Leak(got, secret) {
					t.Errorf("%s: Leak(%q) = false, want true", name, got)
				}
			}
			if got := render("login " + other + " failed"); Leak(got, secret) {
				t.Errorf("%s of other text: Leak(%q) = true, want false", name, got)
			}
		}
	})
	for name, size := range map[string]int{"hmac_pads": 32, "hmac_pads_long_key": sha256.BlockSize + 1} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			key := make([]byte, size)
			unrelated := make([]byte, size)
			for i := range key {
				key[i] = byte(i*7 + 1)
				unrelated[i] = byte(i*11 + 200)
			}
			for pad, block := range hmacPads(key) {
				for route, got := range map[string]string{
					"clear":  string(block),
					"hex":    hex.EncodeToString(block),
					"base64": base64.StdEncoding.EncodeToString(block),
				} {
					if !Leak(got, string(key)) {
						t.Errorf("Leak(%s %s, key) = false, want true", pad, route)
					}
				}
			}
			if Leak(hex.EncodeToString(unrelated)+base64.StdEncoding.EncodeToString(unrelated), string(key)) {
				t.Errorf("Leak(an unrelated %d-byte value, key) = true, want false", size)
			}
			// %v prints the pads a crypto/hmac value caches as decimal byte lists.
			if got := fmt.Sprintf("%v", hmac.New(sha256.New, key)); !Leak(got, string(key)) {
				t.Errorf("Leak(%%v of an HMAC, key) = false, want true: %s", got)
			}
			if got := fmt.Sprintf("%v", hmac.New(sha256.New, unrelated)); Leak(got, string(key)) {
				t.Errorf("Leak(%%v of an HMAC under an unrelated key, key) = true, want false: %s", got)
			}
		})
	}
}

// hmacPads returns the inner and outer pads crypto/hmac with SHA-256 caches
// for key, which a printed HMAC shows: the key, hashed first when longer than
// the block, zero-padded to the block and XORed with 0x36 and 0x5c (RFC 2104
// section 2).
func hmacPads(key []byte) map[string][]byte {
	if len(key) > sha256.BlockSize {
		sum := sha256.Sum256(key)
		key = sum[:]
	}
	padded := func(b byte) []byte {
		block := make([]byte, sha256.BlockSize)
		copy(block, key)
		for i := range block {
			block[i] ^= b
		}
		return block
	}
	return map[string][]byte{"ipad": padded(0x36), "opad": padded(0x5c)}
}
