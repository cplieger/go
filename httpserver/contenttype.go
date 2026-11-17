package httpserver

import (
	"maps"
	"path"
)

const htmlExt = ".html"

// contentTypes is the one table StaticHandler and SPA answer from, never
// mime.TypeByExtension, which reads the host's registry (Windows can map .js
// to application/javascript). .js is text/javascript per RFC 9239.
var contentTypes = map[string]string{
	htmlExt:        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".webmanifest": "application/manifest+json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".webp":        "image/webp",
	".avif":        "image/avif",
	".ico":         "image/vnd.microsoft.icon",
	".woff2":       "font/woff2",
	".txt":         "text/plain; charset=utf-8",
	".wasm":        "application/wasm",
}

// ContentTypes returns a copy of the fixed content-type table StaticHandler
// and SPA answer from, keyed by extension with its dot (".js"). A file whose
// extension is outside it answers application/octet-stream from
// StaticHandler; a bundler refuses to write one.
func ContentTypes() map[string]string { return maps.Clone(contentTypes) }

func contentTypeOf(name string) string {
	if ct, ok := contentTypes[path.Ext(name)]; ok {
		return ct
	}
	return "application/octet-stream"
}
