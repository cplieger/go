package httpwire

import (
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// spoolPrefix names every spool file, so New's cleanup can tell them from
// the other files of an app's data directory.
const spoolPrefix = "httpwire-spool-"

// cleanSpool removes the spool files a crash left in dir: regular files
// whose names start with spoolPrefix, never a file of another name, a
// directory or a symlink.
func cleanSpool(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasPrefix(e.Name(), spoolPrefix) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// spool reads a Keyed Raw row's body through SHA-256 into a new spool
// file, held to limit, and answers the file rewound and the body's digest.
// The caller removes the file with unspool, the error answers included.
func (a *API) spool(w http.ResponseWriter, r *http.Request, ep *Endpoint, limit int) (*os.File, []byte, *Error) {
	f, err := os.CreateTemp(a.o.spoolDir, spoolPrefix+"*")
	if err != nil {
		a.log().ErrorContext(r.Context(), "httpwire: could not create a spool file", "row", ep.Name, "error", err)
		return nil, nil, &Error{Code: codeInternal}
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), http.MaxBytesReader(w, r.Body, int64(limit)))
	if _, over := errors.AsType[*http.MaxBytesError](err); over {
		return f, nil, tooLarge(limit)
	}
	level := slog.LevelError
	if err != nil && r.Context().Err() != nil {
		level = slog.LevelDebug
	}
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		a.log().Log(r.Context(), level, "httpwire: could not spool a request body", "row", ep.Name, "error", err)
		return f, nil, &Error{Code: codeInternal}
	}
	return f, h.Sum(nil), nil
}

func (a *API) unspool(r *http.Request, f *os.File) {
	_ = f.Close()
	if err := os.Remove(f.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		a.log().ErrorContext(r.Context(), "httpwire: could not remove a spool file", "file", f.Name(), "error", err)
	}
}
