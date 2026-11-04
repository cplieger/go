// Package capture provides a slog.Handler that records log records for
// assertion in tests, plus helpers to install it, or any logger, as the
// default logger for one test.
//
// It is a separate package from slogx so that the testing import and the record
// buffer never reach slogx's production consumers. Import it only from _test.go
// files.
//
// Attributes and groups added through Logger.With / Logger.WithGroup are
// captured with the same nesting a real handler would emit: derived handles
// share the recorder's buffer and materialize their inherited context into
// each stored record, so Records reflects what production output would
// contain. Use Records for anything the convenience methods do not cover.
//
// Captured records are render-faithful: they hold what a stdlib TextHandler
// or JSONHandler would emit, not the verbatim call-site input. At ingestion
// the recorder applies the attribute output rules of the slog.Handler
// contract. Values are resolved (slog.LogValuer), a zero Attr is dropped, a
// group with no attrs is dropped, and an empty-keyed group has its attrs
// inlined into its parent.
package capture

import (
	"context"
	"log"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Recorder is a slog.Handler that captures every record it receives, at any
// level, so a test can assert on what was logged. Handler derivation is
// honored per the slog.Handler contract: handles returned by WithAttrs and
// WithGroup record through the same Recorder, with their inherited attributes
// and groups materialized into each stored record. It is safe for concurrent
// use, and the zero value is ready to use: &Recorder{} is a working handler
// (New and Default are conveniences, not required constructors).
type Recorder struct {
	records []slog.Record
	mu      sync.Mutex
}

// New returns an *slog.Logger and the Recorder that captures its output, for code that
// takes an injected logger. It does not touch the global default logger, so a
// test using it may run in parallel.
func New() (*slog.Logger, *Recorder) {
	rec := &Recorder{}
	return slog.New(rec), rec
}

// Default installs a fresh Recorder as slog's default logger until the test
// ends, restoring what SwapDefault restores. Use it for code that logs through
// slog.Default(). Because it mutates global state, a test using it must NOT
// call t.Parallel.
func Default(tb testing.TB) *Recorder {
	tb.Helper()
	rec := &Recorder{}
	SwapDefault(tb, slog.New(rec))
	return rec
}

// SwapDefault makes l slog's default logger until the test ends, then restores
// (via tb.Cleanup) every global slog.SetDefault mutates: slog's default logger,
// and the log package's output writer and flags. Swaps nested in subtests
// restore in reverse order. l must not be nil (slog.SetDefault panics on it).
// Because it mutates global state, a test using it must NOT call t.Parallel.
func SwapDefault(tb testing.TB, l *slog.Logger) {
	tb.Helper()
	// slog.SetDefault also points the log package at the installed handler
	// (log.SetOutput plus log.SetFlags(0)), and skips that redirect when the
	// logger carries slog's own default handler — which prev normally does, so
	// reinstalling prev alone leaves log writing into l. Restore slog first:
	// restoring a non-default prev redirects log again.
	prev, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(l)
	tb.Cleanup(func() {
		slog.SetDefault(prev)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
}

// Enabled reports true for every level so nothing is filtered before capture.
func (rec *Recorder) Enabled(context.Context, slog.Level) bool { return true }

// Handle records r as a stdlib handler would emit it: attribute values are
// resolved and degenerate attrs are dropped or inlined per the slog.Handler
// contract (see normalizeAttrs). Time, Level, Message, and PC are stored
// unchanged. It satisfies slog.Handler.
//
//nolint:gocritic // Handle's signature is fixed by the slog.Handler interface; r cannot be a pointer.
func (rec *Recorder) Handle(_ context.Context, r slog.Record) error {
	nr := materialize(&r)
	rec.append(&nr)
	return nil
}

// append stores one materialized record. All handles derived from this
// Recorder funnel through it, so the buffer stays ordered and race-free.
func (rec *Recorder) append(r *slog.Record) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.records = append(rec.records, *r)
}

// WithAttrs returns a handler that records through this Recorder with attrs
// prepended to every captured record, exactly as a real handler would include
// them. Like the stdlib handlers, it normalizes attrs at derivation time (see
// normalizeAttrs); a slice that is empty or normalizes to empty returns the
// receiver.
func (rec *Recorder) WithAttrs(attrs []slog.Attr) slog.Handler {
	attrs = normalizeAttrs(attrs)
	if len(attrs) == 0 {
		return rec
	}
	return &derived{rec: rec, ops: []op{{attrs: attrs}}}
}

// WithGroup returns a handler that records through this Recorder with
// subsequent attributes nested under name, exactly as a real handler would
// qualify them. An empty name returns the receiver, per the slog.Handler
// contract.
func (rec *Recorder) WithGroup(name string) slog.Handler {
	if name == "" {
		return rec
	}
	return &derived{rec: rec, ops: []op{{group: name}}}
}

// op is one derivation step: a WithAttrs attribute set (attrs non-empty) or a
// WithGroup name (group non-empty). Exactly one field is set.
type op struct {
	group string
	attrs []slog.Attr
}

// derived is the handle WithAttrs/WithGroup return: an immutable derivation
// prefix over the root Recorder's shared record buffer.
type derived struct {
	rec *Recorder
	ops []op
}

// Enabled reports true for every level so nothing is filtered before capture.
func (d *derived) Enabled(context.Context, slog.Level) bool { return true }

// Handle materializes the derivation prefix into the record — inherited
// attributes at their nesting depth, later attributes and the record's own
// under every group opened before them — and stores the result, so the
// captured record matches what a real handler would emit. The record's own
// attrs are normalized first (see normalizeAttrs), so an enclosing group
// whose content normalizes to nothing is elided like the stdlib handlers
// elide it.
//
//nolint:gocritic // Handle's signature is fixed by the slog.Handler interface; r cannot be a pointer.
func (d *derived) Handle(_ context.Context, r slog.Record) error {
	attrs := normalizedRecordAttrs(&r)
	for _, o := range slices.Backward(d.ops) {
		switch {
		case o.group != "":
			if len(attrs) == 0 {
				continue // a group holding no attrs is elided, like the stdlib handlers do
			}
			attrs = []slog.Attr{{Key: o.group, Value: slog.GroupValue(attrs...)}}
		default:
			attrs = slices.Concat(o.attrs, attrs)
		}
	}
	nr := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	nr.AddAttrs(attrs...)
	d.rec.append(&nr)
	return nil
}

// WithAttrs returns a new handle extending the derivation with attrs,
// normalized at derivation time exactly as Recorder.WithAttrs normalizes
// them. A slice that is empty or normalizes to empty returns the receiver.
func (d *derived) WithAttrs(attrs []slog.Attr) slog.Handler {
	attrs = normalizeAttrs(attrs)
	if len(attrs) == 0 {
		return d
	}
	return &derived{rec: d.rec, ops: appendOp(d.ops, op{attrs: attrs})}
}

// WithGroup returns a new handle extending the derivation with a group. An
// empty name returns the receiver, per the slog.Handler contract.
func (d *derived) WithGroup(name string) slog.Handler {
	if name == "" {
		return d
	}
	return &derived{rec: d.rec, ops: appendOp(d.ops, op{group: name})}
}

// appendOp extends a derivation prefix into fresh backing storage, so sibling
// handles derived from the same parent never alias each other's steps.
func appendOp(ops []op, next op) []op {
	out := make([]op, len(ops)+1)
	copy(out, ops)
	out[len(ops)] = next
	return out
}

// normalizedRecordAttrs collects r's attributes and passes them through
// normalizeAttrs, yielding the attr list a stdlib handler would render for r.
func normalizedRecordAttrs(r *slog.Record) []slog.Attr {
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	return normalizeAttrs(attrs)
}

// normalizeAttrs applies the attribute output rules of the slog.Handler
// contract, returning the attrs a stdlib handler would render:
//
//   - every value is resolved first (slog.LogValuer chains; Value.Resolve is
//     loop-capped);
//   - a group's attrs are normalized recursively, and a group left with no
//     attrs is dropped — emptiness is judged after inner normalization, so a
//     group holding only degenerate attrs drops too;
//   - an empty-keyed group has its normalized attrs inlined into its parent;
//   - a zero Attr (key and value both zero) is dropped.
//
// Group values are rebuilt via slog.GroupValue on freshly allocated slices,
// so normalized output never aliases caller-owned storage: content is fixed
// at ingestion time the way a real handler's rendered output is.
func normalizeAttrs(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		a.Value = a.Value.Resolve()
		if a.Value.Kind() == slog.KindGroup {
			children := normalizeAttrs(a.Value.Group())
			switch {
			case len(children) == 0: // a group with no attrs is ignored
			case a.Key == "": // an empty-keyed group inlines its attrs
				out = append(out, children...)
			default:
				out = append(out, slog.Attr{Key: a.Key, Value: slog.GroupValue(children...)})
			}
			continue
		}
		if a.Equal(slog.Attr{}) {
			continue // a zero Attr is ignored
		}
		out = append(out, a)
	}
	return out
}

// materialize returns a copy of r rebuilt through normalizedRecordAttrs, so
// every group value sits on freshly allocated backing storage. Handle uses it
// to fix content at ingestion; Records reuses it for snapshot isolation,
// which is sound because normalizeAttrs is idempotent on already-normalized
// attrs (values are already resolved, degenerate attrs already dropped) and
// always rebuilds group values on fresh slices.
func materialize(r *slog.Record) slog.Record {
	nr := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	nr.AddAttrs(normalizedRecordAttrs(r)...)
	return nr
}

// Records returns a snapshot copy of the captured records, in order. Each
// record is rebuilt through materialize so its group values get fresh backing
// storage (slog.Record.Clone does not recursively copy KindGroup values,
// whose Group() result aliases the stored record's mutable slice); mutating
// or extending the result does not affect later captures.
func (rec *Recorder) Records() []slog.Record {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	records := make([]slog.Record, len(rec.records))
	for i := range rec.records {
		records[i] = materialize(&rec.records[i])
	}
	return records
}

// Len returns the number of captured records.
func (rec *Recorder) Len() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.records)
}

// Messages returns the Message of each captured record, in order.
func (rec *Recorder) Messages() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	msgs := make([]string, len(rec.records))
	for i := range rec.records {
		msgs[i] = rec.records[i].Message
	}
	return msgs
}

// Contains reports whether any captured record's Message contains sub.
func (rec *Recorder) Contains(sub string) bool {
	return rec.Count(sub) > 0
}

// Count returns how many captured records have a Message containing sub.
func (rec *Recorder) Count(sub string) int {
	return rec.countMessages(func(msg string) bool { return strings.Contains(msg, sub) })
}

// countMessages returns how many captured records have a Message matching
// the given predicate. It is the shared scan behind Count and CountExact.
func (rec *Recorder) countMessages(match func(string) bool) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	n := 0
	for i := range rec.records {
		if match(rec.records[i].Message) {
			n++
		}
	}
	return n
}

// CountExact returns how many captured records have a Message exactly equal to
// msg. Use it over Count when the message is pinned by an external contract
// (for example a Loki alert rule matching the exact msg value): the substring
// Count would also match a superstring message ("cycle completed with errors"
// matches Count("cycle complete")), silently passing a test the contract
// should fail.
func (rec *Recorder) CountExact(msg string) int {
	return rec.countMessages(func(m string) bool { return m == msg })
}

// --- Attribute-level assertions ---
//
// The attr helpers below assert on a record's TOP-LEVEL attributes (the same
// set a handler receives after WithAttrs derivations are folded in); values
// nested inside groups are out of scope - walk Records() directly for those.
// Values compare by their RENDERED form (slog.Value.String()), so an
// assertion is kind-agnostic: an Int64 7 renders "7", a string "7" renders
// "7", and a test pinning a logged count does not care which the code chose.
// The empty string is a wildcard for both scoping parameters: msgSub ""
// matches every record, key "" matches every attribute.
//
// The Exact-suffixed members (AttrValueExact, AttrValuesExact) scope the
// message by equality instead of substring, the same relation CountExact has
// to Count. There msg is a whole message, so "" is NOT a wildcard: it matches
// a record whose Message is empty and nothing else. key "" stays a wildcard
// for them.
//
// Attr is the one TYPED member of the family, for the assertion the rendered
// helpers cannot make: that a value is of a particular slog.Kind. Reach for it
// when the kind is the contract and for nothing else — a test that only needs
// the text is clearer through AttrValue, and one that needs a whole record's
// shape (its level, its complete key set, several values at once) still wants a
// Records() walk.

// Attr returns the typed value of the first top-level attribute named key on
// a captured record whose Message contains msgSub, in capture order, and
// whether one was found. It is the primitive behind AttrValue, which renders
// this result.
//
// It exists for the contract a rendered comparison cannot express: that a
// value was logged as a particular KIND. slog.Time("at", t) and
// slog.String("at", t.String()) render identically, so AttrValue cannot tell
// a caller which one the code chose, and a JSON handler renders them very
// differently downstream. An assertion on Kind() pins that choice; one on the
// rendered text does not.
//
// A KindGroup value is detached from the recorder's storage before it is
// returned (its attrs are rebuilt on fresh slices, recursively), so holding
// the result across later captures is safe — the same isolation Records()
// gives. Values are already resolved at ingestion, so the result is never
// KindLogValuer. One boundary is inherited rather than closed: a KindAny
// payload is the caller's own object and is returned as-is, exactly as
// Records() leaves it, so mutating that object still changes what a later
// read observes.
func (rec *Recorder) Attr(msgSub, key string) (slog.Value, bool) {
	var value slog.Value
	found := rec.scanAttrValues(msgSub, key, func(v slog.Value) bool {
		value = detachValue(v)
		return true
	})
	return value, found
}

// AttrValue returns the rendered value of the first top-level attribute
// named key on a captured record whose Message contains msgSub, in capture
// order, and whether one was found.
func (rec *Recorder) AttrValue(msgSub, key string) (string, bool) {
	v, ok := rec.Attr(msgSub, key)
	if !ok {
		return "", false
	}
	return v.String(), true
}

// AttrValueExact returns the rendered value of the first top-level attribute
// named key on a captured record whose Message is exactly equal to msg, in
// capture order, and whether one was found. It is to AttrValue what CountExact
// is to Count: use it when the message is pinned by an external contract (a
// Loki alert rule matching the exact msg value), where AttrValue's substring
// scoping also reads the attribute off a SUPERSTRING message —
// AttrValue("cycle complete", "files") answers from "cycle completed with
// errors" if that record was captured first — so the assertion silently
// inspects a record the contract never named.
//
// msg is a whole message, so "" is not the wildcard it is for msgSub: it
// matches a record whose Message is empty and nothing else, exactly as
// CountExact("") does. key "" still matches every attribute.
func (rec *Recorder) AttrValueExact(msg, key string) (string, bool) {
	var value string
	found := rec.scanAttrValuesBy(exactMessage(msg), key, func(v slog.Value) bool {
		value = v.String()
		return true
	})
	return value, found
}

// AttrValuesExact returns the rendered value of EVERY top-level attribute
// named key carried by the captured records whose Message is exactly equal to
// msg, and nil when none matched. Order is capture order across records, and
// within a record the order a handler would render the attributes in
// (WithAttrs-derived attributes first, the call site's own after).
//
// It is the collector the single-value getters cannot stand in for: an
// assertion about a REPEATED log site — one record per retry, per pruned file,
// per polled item — is about the sequence of values, which AttrValueExact
// (first match only) and HasAttr (any match, order-blind) both flatten. The
// alternative is a Records() walk that reimplements the scoping, the
// top-level-only rule and the rendering.
//
// Message scoping is equality, as in AttrValueExact: msg "" matches only an
// empty Message. key "" matches every attribute, so AttrValuesExact(msg, "")
// renders every top-level value of every record with that message.
func (rec *Recorder) AttrValuesExact(msg, key string) []string {
	var values []string
	rec.scanAttrValuesBy(exactMessage(msg), key, func(v slog.Value) bool {
		values = append(values, v.String())
		return false // never accept: the walk runs to the end, collecting every match
	})
	return values
}

// HasAttr reports whether any captured record whose Message contains msgSub
// carries a top-level attribute named key whose rendered value equals
// rendered.
func (rec *Recorder) HasAttr(msgSub, key, rendered string) bool {
	return rec.scanAttrs(msgSub, key, func(v string) bool { return v == rendered })
}

// AttrContains reports whether any captured record whose Message contains
// msgSub carries a top-level attribute named key whose rendered value
// contains sub.
func (rec *Recorder) AttrContains(msgSub, key, sub string) bool {
	return rec.scanAttrs(msgSub, key, func(v string) bool { return strings.Contains(v, sub) })
}

// CountLevel returns how many captured records at exactly level have a
// Message containing msgSub ("" counts every record at that level). It
// exists for level-escalation contracts - a WARN log site that flips to
// ERROR past a threshold - where the assertion is precisely "one ERROR and
// zero WARN of this message", which the level-blind Count family cannot
// express. Message matching is by substring, the same vocabulary as Count,
// Contains, and the attr helpers' msgSub.
func (rec *Recorder) CountLevel(level slog.Level, msgSub string) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	n := 0
	for i := range rec.records {
		if rec.records[i].Level == level && strings.Contains(rec.records[i].Message, msgSub) {
			n++
		}
	}
	return n
}

// scanAttrs is the rendered-form adapter over scanAttrValues, behind HasAttr
// and AttrContains: it renders each visited value so those helpers stay
// kind-agnostic.
func (rec *Recorder) scanAttrs(msgSub, key string, match func(rendered string) bool) bool {
	return rec.scanAttrValues(msgSub, key, func(v slog.Value) bool { return match(v.String()) })
}

// scanAttrValues is the substring-scoped adapter over scanAttrValuesBy,
// behind Attr and the rendered helpers: msgSub "" matches every record, any
// other value matches by substring.
func (rec *Recorder) scanAttrValues(msgSub, key string, match func(slog.Value) bool) bool {
	return rec.scanAttrValuesBy(func(m string) bool {
		return msgSub == "" || strings.Contains(m, msgSub)
	}, key, match)
}

// exactMessage returns the message predicate the Exact-suffixed accessors
// scope by: equality with msg, the same matcher CountExact applies. It is why
// "" is not a wildcard for them — an empty msg matches an empty Message and
// nothing else.
func exactMessage(msg string) func(string) bool {
	return func(m string) bool { return m == msg }
}

// scanAttrValuesBy is the shared walk behind every attr helper: it visits
// captured records in order, scopes by matchMsg on the Message and by
// attr-key-equals-key (key "" = wildcard), and reports whether match accepted
// any value. A true from match stops the scan; a collector that wants every
// match therefore never accepts. Records are stored materialized (Handle folds
// derivations in, resolves every value and drops degenerate attrs at
// ingestion), so the scan reads them in place under the lock. It hands match a
// value that may still alias the record buffer, which is why a caller that
// LETS one escape — Attr, the only one — detaches it first; the rendered
// helpers derive a string and keep nothing.
func (rec *Recorder) scanAttrValuesBy(matchMsg func(string) bool, key string, match func(slog.Value) bool) bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for i := range rec.records {
		if !matchMsg(rec.records[i].Message) {
			continue
		}
		found := false
		rec.records[i].Attrs(func(a slog.Attr) bool {
			if key != "" && a.Key != key {
				return true
			}
			if match(a.Value) {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// detachValue returns v with any group content rebuilt on freshly allocated
// backing storage, so a value handed out of the lock never aliases a stored
// record. Only KindGroup needs it: slog.Value is otherwise a value type over
// immutable content, and Records() documents the same reason for rebuilding
// groups rather than relying on slog.Record.Clone. normalizeAttrs is the
// shared rebuild — idempotent on already-normalized attrs, and recursive, so a
// group nested in a group is detached at every level.
func detachValue(v slog.Value) slog.Value {
	if v.Kind() != slog.KindGroup {
		return v
	}
	return slog.GroupValue(normalizeAttrs(v.Group())...)
}
