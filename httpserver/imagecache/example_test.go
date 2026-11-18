package imagecache_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/cplieger/go/httpserver/v2/imagecache"
)

func Example() {
	dir, err := os.MkdirTemp("", "covers")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		panic(err)
	}
	defer root.Close()

	// The app's Fetch calls its upstream client with its own credential.
	fetch := func(ctx context.Context, k imagecache.Key) (imagecache.Image, bool, error) {
		png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 64)
		return imagecache.Image{Type: "image/png", Data: []byte(png)}, true, nil
	}
	covers, err := imagecache.New(imagecache.Options{Root: root, Fetch: fetch})
	if err != nil {
		panic(err)
	}
	defer func() { _ = covers.Shutdown(context.Background()) }()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /covers/{id}", func(w http.ResponseWriter, r *http.Request) {
		// The app checks the id against its own library before Serve.
		covers.Serve(w, r, imagecache.Key{Space: "radarr-1", ID: r.PathValue("id"), File: "poster-250.jpg"})
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/covers/42", http.NoBody))
	fmt.Println(rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	// Output: 200 image/png private, max-age=86400
}
