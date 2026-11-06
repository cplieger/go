package filesafe

import (
	"fmt"
	"log/slog"
	"os"
)

type cfg struct {
	logger         *slog.Logger
	maxBytes       int64
	mode           os.FileMode
	mkdirMode      os.FileMode
	modeSet        bool
	recursive      bool
	repairOwnedDir bool
}

// Option configures an atomic write, a stale-temp sweep, or EnsurePrivateDir.
// Each option's doc names which operations it applies to; the rest ignore it.
type Option func(*cfg)

// WithLogger sets a custom logger. If not provided, slog.Default() is used.
func WithLogger(l *slog.Logger) Option {
	return func(c *cfg) { c.logger = l }
}

// WithRepairOwnedDir makes EnsurePrivateDir chmod a pre-existing directory
// to 0700 instead of refusing it with ErrModeTooOpen; writes ignore it. The
// repair is sound because ownership by the effective uid is proved first,
// and every other refusal still fires. It is not the default because a wide
// mode on an owned directory can be deliberate. PrivateDir.Repaired reports
// whether a repair happened.
func WithRepairOwnedDir(repair bool) Option {
	return func(c *cfg) { c.repairOwnedDir = repair }
}

// WithRecursive makes CleanupStaleTemps and CleanupStaleTempsInRoot descend
// into subdirectories; writes ignore it. It is off by default because a
// sweep deletes files, so its reach is stated at the call site. A temp sits
// beside its target, so a write to out/example.com/cert.pfx leaves its
// orphan in out/example.com, which only a recursive sweep of out/ finds.
func WithRecursive(recursive bool) Option {
	return func(c *cfg) { c.recursive = recursive }
}

// WithMode makes mode a guarantee: the staging file is created and proved
// owner-only before any data is written, and the final file is proved to
// hold mode, or the write fails matching ErrModeNotStored. Last call wins.
//
// Without it the file is ordinary: created like os.Create (0666 through the
// umask and inherited ACLs) and never chmod'ed or checked. Pass it for any
// file whose readers must be restricted. On Windows a write or probe given
// it returns ErrModeUnsupported before creating anything.
func WithMode(mode os.FileMode) Option {
	return func(c *cfg) {
		c.mode = mode
		c.modeSet = true
	}
}

// WithMkdirMode creates missing parent directories at mode before writing;
// without it a missing parent is an error. The mode is enforced on each
// created directory (a pre-existing one is never chmod'ed), or the write
// fails with ErrModeNotStored. It governs directories only; the file's mode
// is WithMode's. Sweeps and EnsurePrivateDir ignore it. Result.Durable is
// false if a created directory's parent fsync failed. On Windows a write or
// probe given a nonzero mode returns ErrModeUnsupported before creating
// anything; the caller creates a missing parent there.
func WithMkdirMode(mode os.FileMode) Option {
	return func(c *cfg) { c.mkdirMode = mode }
}

// modeOptionsErr refuses WithMode and WithMkdirMode where the platform
// cannot honor them; it runs before an operation creates anything.
func (c *cfg) modeOptionsErr() error {
	if !c.modeSet && c.mkdirMode == 0 {
		return nil
	}
	if err := modesSupported(); err != nil {
		return fmt.Errorf("%w: WithMode or WithMkdirMode given", err)
	}
	return nil
}

// WithMaxBytes caps the content an atomic write stages, the write-side
// mirror of ReadBounded's bound. WriteFile and WriteFileInRoot refuse
// over-cap data before creating the temp; WriteReader, WriteReaderInRoot and
// a PendingFile refuse, whole, the chunk or call that would cross the cap.
// The barrier rechecks the staged file's size, so bytes staged through
// WriteAt or after a Seek cannot publish over the cap. Every refusal matches
// ErrFileTooLarge and leaves the old target intact. n <= 0 means no cap.
func WithMaxBytes(n int64) Option {
	return func(c *cfg) { c.maxBytes = n }
}

func buildCfg(opts []Option) *cfg {
	c := &cfg{}
	for _, o := range opts {
		if o != nil {
			o(c)
		}
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	return c
}
