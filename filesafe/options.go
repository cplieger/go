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
	mkdirAll       bool
	mkdirModeSet   bool
	modeSet        bool
	recursive      bool
	repairOwnedDir bool
}

// WriteOption configures an atomic write (WriteFile, WriteReader,
// NewPendingFile and their InRoot forms) or a writability probe
// (ProbeWritable, ProbeWritableInRoot). A probe does what a write does, so it
// takes the options of the write it stands in for: pass it the same slice.
// Only this package's options implement it.
type WriteOption interface{ applyWrite(*cfg) }

// SweepOption configures CleanupStaleTemps and CleanupStaleTempsInRoot. Only
// this package's options implement it.
type SweepOption interface{ applySweep(*cfg) }

// PrivateDirOption configures EnsurePrivateDir. Only this package's options
// implement it.
type PrivateDirOption interface{ applyPrivateDir(*cfg) }

// Option is accepted by every operation that takes options: WithLogger is
// the one option that applies to writes, probes, sweeps and EnsurePrivateDir
// alike. Every other option has its operation's own type, so passing it to
// an operation it does not configure is a compile error.
type Option interface {
	WriteOption
	SweepOption
	PrivateDirOption
}

// writeOption, sweepOption and privateDirOption adapt a func to one family.
type (
	writeOption      func(*cfg)
	sweepOption      func(*cfg)
	privateDirOption func(*cfg)
)

func (f writeOption) applyWrite(c *cfg)           { f(c) }
func (f sweepOption) applySweep(c *cfg)           { f(c) }
func (f privateDirOption) applyPrivateDir(c *cfg) { f(c) }

// loggerOption implements Option for WithLogger.
type loggerOption struct{ l *slog.Logger }

func (o loggerOption) applyWrite(c *cfg)      { c.logger = o.l }
func (o loggerOption) applySweep(c *cfg)      { c.logger = o.l }
func (o loggerOption) applyPrivateDir(c *cfg) { c.logger = o.l }

// WithLogger sets the logger an operation reports to. A nil logger, or no
// WithLogger at all, uses slog.Default().
func WithLogger(l *slog.Logger) Option { return loggerOption{l: l} }

// WithRepairOwnedDir makes EnsurePrivateDir chmod a pre-existing directory
// to 0700 instead of refusing it with ErrModeTooOpen. The repair is sound
// because ownership by the effective uid is proved first, and every other
// refusal still fires. It is not the default because a wide mode on an owned
// directory can be deliberate. PrivateDirResult.Repaired reports whether a
// repair happened.
func WithRepairOwnedDir(repair bool) PrivateDirOption {
	return privateDirOption(func(c *cfg) { c.repairOwnedDir = repair })
}

// WithRecursive makes CleanupStaleTemps and CleanupStaleTempsInRoot descend
// into subdirectories. It is off by default because a sweep deletes files,
// so its reach is stated at the call site. A temp sits beside its target, so
// a write to out/example.com/cert.pfx leaves its orphan in out/example.com,
// which only a recursive sweep of out/ finds.
func WithRecursive(recursive bool) SweepOption {
	return sweepOption(func(c *cfg) { c.recursive = recursive })
}

// WithMode makes mode a guarantee: the staging file is created and proved
// owner-only before any data is written, and the final file is proved to
// hold mode, or the write fails matching ErrModeNotStored. Last call wins.
//
// Without it the file is ordinary: created like os.Create (0666 through the
// umask and inherited ACLs) and never chmod'ed or checked. Pass it for any
// file whose readers must be restricted. A zero mode would publish a file
// nobody but root can read, so a write or probe given WithMode(0) returns
// ErrInvalidArgument before creating anything, on every platform. On Windows
// a write or probe given a nonzero mode returns ErrModeUnsupported before
// creating anything.
func WithMode(mode os.FileMode) WriteOption {
	return writeOption(func(c *cfg) {
		c.mode = mode
		c.modeSet = true
	})
}

// WithMkdirMode creates missing parent directories at mode before writing;
// without it a missing parent is an error. The mode is enforced on each
// created directory (a pre-existing one is never chmod'ed), or the write
// fails with ErrModeNotStored. It governs directories only; the file's mode
// is WithMode's. Result.Durable is false if a created directory's parent
// fsync failed. A zero mode returns ErrInvalidArgument before creating
// anything, on every platform; WithMkdirAll(false) is the way to create no
// directories. On Windows a write or probe given a nonzero mode returns
// ErrModeUnsupported before creating anything; use WithMkdirAll there. It and WithMkdirAll replace
// each other; the last call wins.
func WithMkdirMode(mode os.FileMode) WriteOption {
	return writeOption(func(c *cfg) { c.mkdirMode, c.mkdirModeSet, c.mkdirAll = mode, true, false })
}

// WithMkdirAll with create true makes a write or probe create missing
// parent directories first, as os.MkdirAll does: 0777 through the umask and
// any inherited ACL, with no mode chmod'ed or checked, on every platform.
// Each created directory is synced into its parent where the platform syncs
// directories; on Windows the write's flush of the published file persists
// them on local NTFS, and Result.Durable is false on other volumes. Use
// WithMkdirMode instead for a directory whose readers must be restricted.
// WithMkdirAll(false) creates nothing. Either way it and WithMkdirMode
// replace each other; the last call wins.
func WithMkdirAll(create bool) WriteOption {
	return writeOption(func(c *cfg) { c.mkdirMode, c.mkdirModeSet, c.mkdirAll = 0, false, create })
}

// parentDirs reports whether a write or probe creates missing parent
// directories, at which mode, and whether it proves that mode on each.
func (c *cfg) parentDirs() (mode os.FileMode, enforce, create bool) {
	switch {
	case c.mkdirModeSet:
		return c.mkdirMode, true, true
	case c.mkdirAll:
		return 0o777, false, true
	default:
		return 0, false, false
	}
}

// writeOptionsErr refuses a zero WithMode or WithMkdirMode, then the two
// mode options where the platform cannot honor them; it runs before an
// operation creates anything.
func (c *cfg) writeOptionsErr() error {
	if c.modeSet && c.mode == 0 {
		return fmt.Errorf("%w: WithMode(0)", ErrInvalidArgument)
	}
	if c.mkdirModeSet && c.mkdirMode == 0 {
		return fmt.Errorf("%w: WithMkdirMode(0)", ErrInvalidArgument)
	}
	if !c.modeSet && !c.mkdirModeSet {
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
// ErrFileTooLarge and leaves the old target intact. n <= 0 means no cap. A
// probe stages one byte, so no cap refuses it.
func WithMaxBytes(n int64) WriteOption {
	return writeOption(func(c *cfg) { c.maxBytes = n })
}

// buildWriteCfg, buildSweepCfg and buildPrivateDirCfg resolve one family's
// options (a nil option is skipped) and apply the defaults.
func buildWriteCfg(opts []WriteOption) *cfg {
	c := &cfg{}
	for _, o := range opts {
		if o != nil {
			o.applyWrite(c)
		}
	}
	return c.withDefaults()
}

func buildSweepCfg(opts []SweepOption) *cfg {
	c := &cfg{}
	for _, o := range opts {
		if o != nil {
			o.applySweep(c)
		}
	}
	return c.withDefaults()
}

func buildPrivateDirCfg(opts []PrivateDirOption) *cfg {
	c := &cfg{}
	for _, o := range opts {
		if o != nil {
			o.applyPrivateDir(c)
		}
	}
	return c.withDefaults()
}

func (c *cfg) withDefaults() *cfg {
	if c.logger == nil {
		c.logger = slog.Default()
	}
	return c
}
