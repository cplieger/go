//go:build unix

package httpwire_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cplieger/go/httpwire"
	"github.com/cplieger/go/sealed"
)

// TestAClosedKeyFileAnswersInternalError closes the key file under a
// running API: its Sealer then holds no key, which is the server's fault,
// so no answer tells the client to drop its cursor.
func TestAClosedKeyFileAnswersInternalError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	kf, err := sealed.OpenKeyFile(t.Context(), filepath.Join(dir, "cursor.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := kf.Create(t.Context()); err != nil {
		t.Fatal(err)
	}
	sink := &logSink{}
	s := newPageStore(t, 10, httpwire.WithSealer(kf.Sealer()), httpwire.WithLogger(slog.New(sink)))
	c := pageOf(t, s.get("/api/items?limit=3", sessionA)).NextCursor
	handle := sealOn(t, s, "/api/handles", sessionA, func(ctx context.Context) string {
		v, err := httpwire.Seal(ctx, "state", []byte("payload"), time.Hour)
		if err != nil {
			return "error " + err.Error()
		}
		return v
	})
	if err := kf.Close(); err != nil {
		t.Fatal(err)
	}
	t.Run("a cursor", func(t *testing.T) {
		n := s.ran()
		rec := s.get(withCursor("/api/items", c, "limit", "3"), sessionA)
		if env := envelopeOf(t, rec); rec.Code != http.StatusInternalServerError || env.Code != "internal_error" {
			t.Errorf("answered %d %q, want 500 internal_error", rec.Code, env.Code)
		}
		if s.ran() != n {
			t.Error("the handler ran")
		}
		if !sink.has("ERROR", "row=items.list") {
			t.Errorf("no ERROR line naming the row: %q", sink.all())
		}
	})
	t.Run("a first page", func(t *testing.T) {
		rec := s.get("/api/items?limit=3", sessionA)
		if env := envelopeOf(t, rec); rec.Code != http.StatusInternalServerError || env.Code != "internal_error" {
			t.Errorf("answered %d %q, want 500 internal_error", rec.Code, env.Code)
		}
	})
	t.Run("Seal", func(t *testing.T) {
		got := sealOn(t, s, "/api/handles", sessionA, func(ctx context.Context) string {
			_, err := httpwire.Seal(ctx, "state", []byte("payload"), time.Hour)
			return strconv.FormatBool(errors.Is(err, sealed.ErrNoKey))
		})
		if got != "true" {
			t.Error("Seal did not answer sealed.ErrNoKey")
		}
	})
	t.Run("Open", func(t *testing.T) {
		got := sealOn(t, s, "/api/handles", sessionA, func(ctx context.Context) string {
			_, err := httpwire.Open(ctx, "state", handle)
			return strconv.FormatBool(errors.Is(err, sealed.ErrNoKey) && !errors.Is(err, httpwire.ErrSealed))
		})
		if got != "true" {
			t.Error("Open did not answer sealed.ErrNoKey, apart from ErrSealed")
		}
	})
}
