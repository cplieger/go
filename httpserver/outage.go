package httpserver

import (
	"io/fs"
	"net/http"
)

type OutageConfig struct {
	Assets    fs.FS
	Errors    ErrorResponder
	Style     string
	Title     string
	App       string
	APIPrefix string
	Files     []OutageFile
	Commands  []string
	Restart   bool
}

type OutageFile struct {
	Name  string
	Class OutageClass
}

type OutageClass string

const (
	OutageDamaged    OutageClass = "damaged"
	OutageFormat     OutageClass = "format"
	OutageNewer      OutageClass = "newer"
	OutageUnreadable OutageClass = "unreadable"
	OutageKey        OutageClass = "key"
)

func OutagePage(c OutageConfig) (http.Handler, error) { //nolint:gocritic // stub
	return http.NotFoundHandler(), nil
}
