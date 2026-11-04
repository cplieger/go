package slogx

import (
	"io"
	"log/slog"
	"os"
	"strconv"
)

// Format selects the slog handler's output encoding.
type Format int

const (
	// Text emits canonical logfmt (time=… level=… msg=… k=v). It is the
	// default for a container's own lifecycle and diagnostic logs.
	Text Format = iota
	// JSON emits one JSON object per line, for apps whose structured log events
	// are the product (shipped to Loki and rendered as dashboard columns).
	JSON
)

// Options configures a handler built by NewHandler or Setup. The zero value is
// valid and yields a text handler at Info level on stderr.
type Options struct {
	// Output is the destination writer; nil means os.Stderr. Apps whose JSON log
	// events are the product typically set os.Stdout.
	Output io.Writer
	// Format is Text (default) or JSON. Any other value is a programmer error
	// and makes NewHandler and Setup panic; parse untrusted format strings with
	// ParseFormat, which only produces the two constants.
	Format Format
	// Level is the initial level; the zero value is slog.LevelInfo. It is held in
	// the *slog.LevelVar that NewHandler and Setup return, so it can be changed
	// after the handler is installed.
	Level slog.Level
	// AddSource records the source file:line of each call site. Off by default —
	// useful when debugging, noisy for production.
	AddSource bool
}

// NewHandler builds a leveled slog.Handler with UTCTime applied and returns it
// alongside the *slog.LevelVar backing its level. Hold the LevelVar to change
// the level after install: install a handler before config is read (so early
// warnings emit at the default level), then Set the parsed level; or flip it at
// runtime for a debug toggle. Setup wraps this and installs the result as the
// default logger. It panics if opts.Format is neither Text nor JSON.
func NewHandler(opts Options) (slog.Handler, *slog.LevelVar) {
	out := opts.Output
	if out == nil {
		out = os.Stderr
	}
	levelVar := new(slog.LevelVar)
	levelVar.Set(opts.Level)
	handlerOpts := &slog.HandlerOptions{
		AddSource:   opts.AddSource,
		Level:       levelVar,
		ReplaceAttr: UTCTime,
	}
	switch opts.Format {
	case Text:
		return slog.NewTextHandler(out, handlerOpts), levelVar
	case JSON:
		return slog.NewJSONHandler(out, handlerOpts), levelVar
	default:
		panic("slogx: invalid Format value " + strconv.Itoa(int(opts.Format)))
	}
}

// Setup builds a handler per opts and installs it as slog's default logger,
// returning the *slog.LevelVar backing its level for later changes (install
// early, then Set the level once config is read; or flip it at runtime). It is
// the one-call standard logger setup most apps make once at startup.
func Setup(opts Options) *slog.LevelVar {
	handler, levelVar := NewHandler(opts)
	slog.SetDefault(slog.New(handler))
	return levelVar
}
