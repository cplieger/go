// Package schedulertest holds the contract every scheduler.Records keeps, as
// one test an implementation's own tests call.
package schedulertest

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/scheduler/v2"
)

// Records runs the Records contract against the stores open answers, one
// new empty store per case. Cases that order records by their end run inside
// a synctest bubble, so open is called there and the store's own clock is the
// bubble's.
func Records(t *testing.T, open func() scheduler.Records) {
	t.Helper()
	t.Run("insert then get", func(t *testing.T) { insertThenGet(t, open()) })
	t.Run("a keyed resend answers the stored record", func(t *testing.T) { keyedResend(t, open()) })
	t.Run("put writes a newer rev only", func(t *testing.T) { putNewerRev(t, open()) })
	t.Run("end writes once", func(t *testing.T) { endOnce(t, open()) })
	t.Run("an ended record takes no put", func(t *testing.T) { putAfterEnd(t, open()) })
	t.Run("dismiss", func(t *testing.T) { dismiss(t, open) })
	t.Run("live", func(t *testing.T) { synctest.Test(t, func(t *testing.T) { live(t, open()) }) })
	t.Run("history pages newest first", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) { history(t, open()) })
	})
	t.Run("last ends", func(t *testing.T) { synctest.Test(t, func(t *testing.T) { lastEnds(t, open()) }) })
}

// The values the cases share.
const (
	credential = "cred"
	row        = "scan.start"
	phaseFetch = scheduler.Phase("fetch")
	unknownID  = scheduler.ID("000000000000000000000000000003e7")
)

// fixture builds records with distinct IDs.
type fixture struct {
	t *testing.T
	r scheduler.Records
	n int
}

func (f *fixture) record(kind scheduler.Kind, scope string) *scheduler.Record {
	f.n++
	return &scheduler.Record{
		ID:      scheduler.ID(fmt.Sprintf("%032x", f.n)),
		Kind:    kind,
		Scope:   scope,
		Trigger: scheduler.TriggerAPI,
		Owner:   "owner",
		State:   scheduler.StateQueued,
		Phase:   scheduler.PhaseQueued,
		Payload: json.RawMessage(fmt.Sprintf(`{"n":%d}`, f.n)),
		Rev:     1,
	}
}

// insert writes a new record and fails the test unless it is new.
func (f *fixture) insert(r *scheduler.Record, key *scheduler.Key) *scheduler.Record {
	f.t.Helper()
	got, replayed, err := f.r.Insert(f.t.Context(), *r, key)
	if err != nil || replayed {
		f.t.Fatalf("Insert(%s) = replayed %v, %v; want a new record", r.ID, replayed, err)
	}
	return &got
}

// start writes a copy of r moved to running at the next Rev.
func (f *fixture) start(r *scheduler.Record) *scheduler.Record {
	f.t.Helper()
	n := *r
	n.State, n.Phase, n.StartedAt, n.Rev = scheduler.StateRunning, scheduler.PhaseStarted, time.Now(), r.Rev+1
	f.put(&n)
	return f.get(n.ID)
}

func (f *fixture) put(r *scheduler.Record) {
	f.t.Helper()
	if err := f.r.Put(f.t.Context(), *r); err != nil {
		f.t.Fatalf("Put(%s rev %d) = %v", r.ID, r.Rev, err)
	}
}

func (f *fixture) end(id scheduler.ID, e scheduler.End) *scheduler.Record {
	f.t.Helper()
	got, err := f.r.End(f.t.Context(), id, e)
	if err != nil {
		f.t.Fatalf("End(%s) = %v", id, err)
	}
	return &got
}

func (f *fixture) get(id scheduler.ID) *scheduler.Record {
	f.t.Helper()
	got, ok, err := f.r.Get(f.t.Context(), id)
	if err != nil || !ok {
		f.t.Fatalf("Get(%s) = found %v, %v; want the record", id, ok, err)
	}
	return &got
}

// ran inserts, starts and ends one job of kind and scope.
func (f *fixture) ran(kind scheduler.Kind, scope string, res scheduler.Result) *scheduler.Record {
	f.t.Helper()
	r := f.start(f.insert(f.record(kind, scope), nil))
	return f.end(r.ID, scheduler.End{Result: res})
}

func insertThenGet(t *testing.T, r scheduler.Records) {
	f := &fixture{t: t, r: r}
	want := f.record("scan", "source:a")
	got := f.insert(want, nil)
	if got.ID != want.ID || got.State != scheduler.StateQueued || got.QueuedAt.IsZero() {
		t.Errorf("Insert answered id %s state %q queued at %v; want id %s, queued, a queued time",
			got.ID, got.State, got.QueuedAt, want.ID)
	}
	stored := f.get(want.ID)
	if stored.Kind != want.Kind || stored.Scope != want.Scope || stored.Owner != want.Owner ||
		stored.Trigger != want.Trigger || string(stored.Payload) != string(want.Payload) || stored.Key != nil {
		t.Errorf("Get = %+v; want the inserted kind, scope, owner, trigger and payload, no key", stored)
	}
	if _, ok, err := r.Get(t.Context(), unknownID); ok || err != nil {
		t.Errorf("Get(an unknown id) = found %v, %v; want not found, nil", ok, err)
	}
}

func keyedResend(t *testing.T, r scheduler.Records) {
	f := &fixture{t: t, r: r}
	key := &scheduler.Key{Credential: credential, Row: row, Key: "k1", Fingerprint: [32]byte{1}}
	first := f.insert(f.record("scan", ""), key)
	if stored := f.get(first.ID); stored.Key == nil || *stored.Key != *key {
		t.Errorf("the stored key = %+v, want %+v", stored.Key, key)
	}

	resend := f.record("scan", "")
	got, replayed, err := r.Insert(t.Context(), *resend, key)
	if err != nil || !replayed || got.ID != first.ID {
		t.Errorf("a resend with the same key = id %s replayed %v, %v; want id %s, true, nil", got.ID, replayed, err, first.ID)
	}
	if _, ok, _ := r.Get(t.Context(), resend.ID); ok {
		t.Error("a resend wrote its own record, want none")
	}

	if got, ok, err := r.ByKey(t.Context(), credential, row, "k1"); err != nil || !ok || got.ID != first.ID {
		t.Errorf("ByKey = id %s found %v, %v; want id %s", got.ID, ok, err, first.ID)
	}
	for name, k := range map[string][3]string{
		"another credential": {"other", row, "k1"},
		"another row":        {credential, "refresh.start", "k1"},
		"another key":        {credential, row, "k2"},
	} {
		if _, ok, err := r.ByKey(t.Context(), k[0], k[1], k[2]); ok || err != nil {
			t.Errorf("ByKey with %s = found %v, %v; want not found, nil", name, ok, err)
		}
	}
}

func putNewerRev(t *testing.T, r scheduler.Records) {
	f := &fixture{t: t, r: r}
	rec := f.start(f.insert(f.record("scan", ""), nil))
	if rec.State != scheduler.StateRunning || rec.Phase != scheduler.PhaseStarted || rec.StartedAt.IsZero() {
		t.Errorf("after a newer Put: state %q phase %q started %v; want running, started, a start time",
			rec.State, rec.Phase, rec.StartedAt)
	}

	stale := *rec
	stale.Phase = "stale"
	f.put(&stale) // the same Rev
	stale.Rev--
	f.put(&stale)
	if got := f.get(rec.ID); got.Phase != scheduler.PhaseStarted {
		t.Errorf("after a Put of the same and an older Rev: phase %q, want %q", got.Phase, scheduler.PhaseStarted)
	}

	next := *rec
	next.Phase, next.Rev = phaseFetch, rec.Rev+1
	next.Phases = []scheduler.PhaseAt{{Phase: scheduler.PhaseStarted, At: rec.StartedAt}, {Phase: phaseFetch, At: time.Now()}}
	next.Step = struct{ N, Of int }{1, 3}
	next.Proposal = "proposal"
	f.put(&next)
	got := f.get(rec.ID)
	if got.Phase != phaseFetch || got.Step.N != 1 || got.Step.Of != 3 || len(got.Phases) != 2 || got.Phases[1].Phase != phaseFetch {
		t.Errorf("after a phase Put: phase %q step %+v phases %+v; want fetch, 1 of 3, two phases", got.Phase, got.Step, got.Phases)
	}
	if got.Proposal != next.Proposal {
		t.Errorf("after a Put naming a proposal: Proposal %q, want %q", got.Proposal, next.Proposal)
	}
}

func endOnce(t *testing.T, r scheduler.Records) {
	f := &fixture{t: t, r: r}
	rec := f.start(f.insert(f.record("scan", ""), nil))
	first := scheduler.End{Result: scheduler.ResultOK, Facts: json.RawMessage(`{"found":2}`)}
	got := f.end(rec.ID, first)
	if got.State != scheduler.StateEnded || got.End == nil || got.End.Result != scheduler.ResultOK || got.EndedAt.IsZero() {
		t.Fatalf("End answered state %q end %+v ended %v; want ended ok at a time", got.State, got.End, got.EndedAt)
	}

	again := f.end(rec.ID, scheduler.End{Result: scheduler.ResultFailed})
	if again.End == nil || again.End.Result != scheduler.ResultOK || string(again.End.Facts) != `{"found":2}` ||
		!again.EndedAt.Equal(got.EndedAt) {
		t.Errorf("a second End answered %+v at %v; want the first end, ok with its facts, at %v", again.End, again.EndedAt, got.EndedAt)
	}
	if stored := f.get(rec.ID); stored.End == nil || stored.End.Result != scheduler.ResultOK {
		t.Errorf("after a second End the stored end is %+v, want the first", stored.End)
	}

	if _, err := r.End(t.Context(), unknownID, first); !errors.Is(err, scheduler.ErrUnknownJob) {
		t.Errorf("End(an unknown id) = %v, want ErrUnknownJob", err)
	}
}

func putAfterEnd(t *testing.T, r scheduler.Records) {
	f := &fixture{t: t, r: r}
	rec := f.start(f.insert(f.record("scan", ""), nil))
	ended := f.end(rec.ID, scheduler.End{Result: scheduler.ResultOK})

	late := *rec
	late.State, late.Phase, late.Rev = scheduler.StateRunning, phaseFetch, ended.Rev+10
	f.put(&late)
	if got := f.get(rec.ID); got.State != scheduler.StateEnded || got.End == nil || got.Phase == phaseFetch {
		t.Errorf("a Put after the end left state %q phase %q end %+v; want the ended record unchanged", got.State, got.Phase, got.End)
	}
}

func dismiss(t *testing.T, open func() scheduler.Records) {
	t.Run("a review line becomes skipped and keeps its reason", func(t *testing.T) { dismissReview(t, open()) })
	t.Run("a repeat writes nothing", func(t *testing.T) { dismissRepeat(t, open()) })
	t.Run("out of range and unknown", func(t *testing.T) { dismissUnknown(t, open()) })
}

// batch writes an ended partial batch whose line 1 is in review.
func (f *fixture) batch() *scheduler.Record {
	f.t.Helper()
	rec := f.start(f.insert(f.record("grab", ""), nil))
	rec.Lines = []scheduler.Line{
		{State: scheduler.LineDone},
		{State: scheduler.LineReview, Reason: "arr_rejected", Facts: json.RawMessage(`{"code":400}`)},
		{State: scheduler.LineSkipped, Reason: "nothing_found"},
	}
	rec.Rev++
	f.put(rec)
	return f.end(rec.ID, scheduler.End{Result: scheduler.ResultPartial})
}

func dismissReview(t *testing.T, r scheduler.Records) {
	f := &fixture{t: t, r: r}
	before := f.batch()
	if err := r.Dismiss(t.Context(), before.ID, 1); err != nil {
		t.Fatalf("Dismiss(line 1) = %v, want nil", err)
	}
	got := f.get(before.ID)
	if len(got.Lines) != 3 {
		t.Fatalf("after Dismiss: %d lines, want the 3 still there", len(got.Lines))
	}
	if l := got.Lines[1]; l.State != scheduler.LineSkipped || l.Reason != "arr_rejected" || string(l.Facts) != `{"code":400}` {
		t.Errorf("the dismissed line = %+v; want skipped, reason arr_rejected, its facts", l)
	}
	if got.Lines[0].State != scheduler.LineDone || got.Lines[2].State != scheduler.LineSkipped {
		t.Errorf("the other lines = %+v, %+v; want them unchanged", got.Lines[0], got.Lines[2])
	}
	if got.Rev <= before.Rev {
		t.Errorf("Rev after Dismiss = %d, want above %d", got.Rev, before.Rev)
	}
	if got.End == nil || got.End.Result != scheduler.ResultPartial || got.State != scheduler.StateEnded {
		t.Errorf("after Dismiss the end is %+v state %q; want partial and ended still", got.End, got.State)
	}
}

func dismissRepeat(t *testing.T, r scheduler.Records) {
	f := &fixture{t: t, r: r}
	rec := f.batch()
	if err := r.Dismiss(t.Context(), rec.ID, 1); err != nil {
		t.Fatalf("Dismiss = %v", err)
	}
	once := f.get(rec.ID)
	for _, line := range []int{1, 0, 2} {
		if err := r.Dismiss(t.Context(), rec.ID, line); err != nil {
			t.Errorf("Dismiss(line %d, not in review) = %v, want nil", line, err)
		}
	}
	if got := f.get(rec.ID); got.Rev != once.Rev || !slices.EqualFunc(got.Lines, once.Lines, sameLine) {
		t.Errorf("after a repeat: rev %d lines %+v; want rev %d lines %+v", got.Rev, got.Lines, once.Rev, once.Lines)
	}
}

func dismissUnknown(t *testing.T, r scheduler.Records) {
	f := &fixture{t: t, r: r}
	rec := f.batch()
	plain := f.ran("scan", "", scheduler.ResultOK)
	for name, c := range map[string]struct {
		want error
		id   scheduler.ID
		line int
	}{
		"past the last line":     {scheduler.ErrUnknownLine, rec.ID, 3},
		"a negative line":        {scheduler.ErrUnknownLine, rec.ID, -1},
		"a record with no lines": {scheduler.ErrUnknownLine, plain.ID, 0},
		"an unknown id":          {scheduler.ErrUnknownJob, unknownID, 0},
	} {
		if err := r.Dismiss(t.Context(), c.id, c.line); !errors.Is(err, c.want) {
			t.Errorf("Dismiss with %s = %v, want %v", name, err, c.want)
		}
	}
}

func sameLine(a, b scheduler.Line) bool {
	return a.State == b.State && a.Reason == b.Reason && string(a.Facts) == string(b.Facts)
}

func live(t *testing.T, r scheduler.Records) {
	f := &fixture{t: t, r: r}
	queued := f.insert(f.record("scan", "a"), nil)
	running := f.start(f.insert(f.record("scan", "b"), nil))
	old := f.ran("scan", "c", scheduler.ResultOK)
	time.Sleep(time.Minute)
	ended := make([]scheduler.ID, 0, 51)
	for range 51 {
		ended = append(ended, f.ran("scan", "d", scheduler.ResultOK).ID)
		time.Sleep(time.Second)
	}

	got, err := r.Live(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("Live = %v", err)
	}
	ids := idsOf(got)
	for name, id := range map[string]scheduler.ID{"queued": queued.ID, "running": running.ID} {
		if !slices.Contains(ids, id) {
			t.Errorf("Live misses the %s record", name)
		}
	}
	if slices.Contains(ids, old.ID) || slices.Contains(ids, ended[0]) {
		t.Error("Live holds an ended record past the newest 50")
	}
	for _, id := range ended[1:] {
		if !slices.Contains(ids, id) {
			t.Errorf("Live misses ended record %s, one of the newest 50", id)
		}
	}
	if len(got) != 52 {
		t.Errorf("Live answered %d records, want 2 not ended and 50 ended", len(got))
	}

	got, err = r.Live(t.Context(), time.Now().Add(15*time.Minute+51*time.Second))
	if err != nil {
		t.Fatalf("Live = %v", err)
	}
	if ids := idsOf(got); len(ids) != 2 || !slices.Contains(ids, queued.ID) || !slices.Contains(ids, running.ID) {
		t.Errorf("Live 15 minutes after every end = %v, want only the queued and running records", ids)
	}
}

func history(t *testing.T, r scheduler.Records) {
	f := &fixture{t: t, r: r}
	scans := make([]scheduler.ID, 0, 5)
	for i := range 5 {
		scans = append(scans, f.ran("scan", "", scheduler.ResultOK).ID)
		time.Sleep(time.Second)
		if i == 2 {
			f.ran("refresh", "", scheduler.ResultOK)
			time.Sleep(time.Second)
		}
	}
	f.start(f.insert(f.record("scan", ""), nil))
	slices.Reverse(scans)

	var pages [][]scheduler.ID
	after := ""
	for range 4 {
		page, next, err := r.History(t.Context(), "scan", after, 2)
		if err != nil {
			t.Fatalf("History(after %q) = %v", after, err)
		}
		pages = append(pages, idsOf(page))
		if next == "" {
			break
		}
		after = next
	}
	want := [][]scheduler.ID{scans[:2], scans[2:4], scans[4:]}
	if !slices.EqualFunc(pages, want, slices.Equal[[]scheduler.ID]) {
		t.Errorf("History(scan) in pages of 2 = %v, want newest first %v", pages, want)
	}

	all, next, err := r.History(t.Context(), "", "", 10)
	if err != nil || next != "" || len(all) != 6 {
		t.Errorf("History(every kind) = %d records, next %q, %v; want the 6 ended, the last page", len(all), next, err)
	}
}

func lastEnds(t *testing.T, r scheduler.Records) {
	f := &fixture{t: t, r: r}
	f.ran("scan", "a", scheduler.ResultOK)
	time.Sleep(time.Second)
	failed := f.ran("scan", "a", scheduler.ResultFailed)
	time.Sleep(time.Second)
	f.ran("scan", "a", scheduler.ResultCancelled)
	time.Sleep(time.Second)
	f.ran("scan", "a", scheduler.ResultInterrupted)
	time.Sleep(time.Second)
	never := f.insert(f.record("scan", "a"), nil)
	f.end(never.ID, scheduler.End{Result: scheduler.ResultSkipped, Reasons: []scheduler.Reason{{Code: scheduler.ReasonCovered}}})

	skipped := f.ran("check", "", scheduler.ResultSkipped)
	onlyCancelled := f.start(f.insert(f.record("check", "b"), nil))
	f.end(onlyCancelled.ID, scheduler.End{Result: scheduler.ResultCancelled})

	got, err := r.LastEnds(t.Context())
	if err != nil {
		t.Fatalf("LastEnds = %v", err)
	}
	if le, ok := got[scheduler.ScheduledScope{Kind: "scan", Scope: "a"}]; !ok || le.Result != scheduler.ResultFailed || !le.At.Equal(failed.EndedAt) {
		t.Errorf("LastEnds[scan a] = %+v found %v; want the failed end at %v, past the cancelled, interrupted and never-started ends",
			le, ok, failed.EndedAt)
	}
	if le, ok := got[scheduler.ScheduledScope{Kind: "check"}]; !ok || le.Result != scheduler.ResultSkipped || !le.At.Equal(skipped.EndedAt) {
		t.Errorf("LastEnds[check] = %+v found %v; want the skipped end its run returned at %v", le, ok, skipped.EndedAt)
	}
	if le, ok := got[scheduler.ScheduledScope{Kind: "check", Scope: "b"}]; ok {
		t.Errorf("LastEnds[check b] = %+v; want none, its only end was cancelled", le)
	}
}

func idsOf(rs []scheduler.Record) []scheduler.ID {
	ids := make([]scheduler.ID, 0, len(rs))
	for i := range rs {
		ids = append(ids, rs[i].ID)
	}
	return ids
}
