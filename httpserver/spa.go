package httpserver

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
)

// SPAConfig configures SPA.
type SPAConfig struct {
	Assets    fs.FS
	CSPAdd    map[string][]string
	Errors    ErrorResponder
	Logger    *slog.Logger
	APIPrefix string
	Pages     []Page
}

// SPA serves a single-page app's bundle.
func SPA(c SPAConfig) (http.Handler, error) { return nil, errors.New("not implemented") }
