package schedulertest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/go/scheduler/v2"
)

// EndInFunc is a store's own-commit end: the job's end written in the app's transaction.
type EndInFunc func(ctx context.Context, id scheduler.ID, e scheduler.End) error

// EndIn runs the own-commit end's contract against the stores open answers,
// one new empty store per case: the end is closed by scheduler.CloseEnd, its
// batch's open lines and its bounds alike, a later Records.End keeps it, and
// a record that is not running is refused with nothing written.
func EndIn(t *testing.T, open func() (scheduler.Records, EndInFunc)) {
	t.Helper()
	t.Run("an end closes the open lines", func(t *testing.T) { endInCloses(t, open) })
	t.Run("an ok end over ended lines", func(t *testing.T) { endInEnded(t, open) })
	t.Run("an end past its bounds is cut", func(t *testing.T) { endInCut(t, open) })
	t.Run("a later end keeps it", func(t *testing.T) {
		r, endIn := open()
		endInKept(t, r, endIn)
	})
	t.Run("a record not running is refused", func(t *testing.T) {
		r, endIn := open()
		endInRefused(t, r, endIn)
	})
}

// running writes a started batch holding lines.
func (f *fixture) running(lines ...scheduler.Line) *scheduler.Record {
	f.t.Helper()
	rec := f.start(f.insert(f.record("grab", ""), nil))
	rec.Lines = lines
	f.put(rec)
	return f.get(rec.ID)
}

func (f *fixture) endIn(endIn EndInFunc, id scheduler.ID, e scheduler.End) *scheduler.Record {
	f.t.Helper()
	if err := endIn(f.t.Context(), id, e); err != nil {
		f.t.Fatalf("EndIn(%s, %s) = %v", id, e.Result, err)
	}
	return f.get(id)
}

func wantLines(t *testing.T, got []scheduler.Line, want ...scheduler.Line) {
	t.Helper()
	if !slices.EqualFunc(got, want, sameLine) {
		t.Errorf("lines %+v, want %+v", got, want)
	}
}

func endInCloses(t *testing.T, open func() (scheduler.Records, EndInFunc)) {
	done := scheduler.Line{State: scheduler.LineDone, Facts: json.RawMessage(`{"id":1}`)}
	for _, tc := range []struct {
		end, want scheduler.Result
		reason    string
	}{
		{end: scheduler.ResultOK, want: scheduler.ResultPartial, reason: scheduler.ReasonNotRun},
		{end: scheduler.ResultFailed, want: scheduler.ResultFailed, reason: scheduler.ReasonNotRun},
		{end: scheduler.ResultCancelled, want: scheduler.ResultCancelled, reason: scheduler.ReasonCancelled},
		{end: scheduler.ResultInterrupted, want: scheduler.ResultInterrupted, reason: scheduler.ReasonInterrupted},
	} {
		t.Run(string(tc.end), func(t *testing.T) {
			r, endIn := open()
			f := &fixture{t: t, r: r}
			rec := f.running(done, scheduler.Line{State: scheduler.LineRunning}, scheduler.Line{State: scheduler.LineQueued})
			got := f.endIn(endIn, rec.ID, scheduler.End{Result: tc.end})
			if got.State != scheduler.StateEnded || got.End == nil || got.End.Result != tc.want {
				t.Errorf("state %q end %+v, want ended %s", got.State, got.End, tc.want)
			}
			review := scheduler.Line{State: scheduler.LineReview, Reason: tc.reason}
			wantLines(t, got.Lines, done, review, review)
		})
	}
}

func endInEnded(t *testing.T, open func() (scheduler.Records, EndInFunc)) {
	for name, tc := range map[string]struct {
		want scheduler.Result
		line scheduler.Line
	}{
		"every line done stays ok":       {line: scheduler.Line{State: scheduler.LineDone}, want: scheduler.ResultOK},
		"a skipped line reads partial":   {line: scheduler.Line{State: scheduler.LineSkipped, Reason: "gone"}, want: scheduler.ResultPartial},
		"a line in review reads partial": {line: scheduler.Line{State: scheduler.LineReview, Reason: "rejected"}, want: scheduler.ResultPartial},
	} {
		t.Run(name, func(t *testing.T) {
			r, endIn := open()
			f := &fixture{t: t, r: r}
			rec := f.running(scheduler.Line{State: scheduler.LineDone}, tc.line)
			got := f.endIn(endIn, rec.ID, scheduler.End{Result: scheduler.ResultOK})
			if got.End == nil || got.End.Result != tc.want {
				t.Errorf("end %+v, want %s", got.End, tc.want)
			}
			wantLines(t, got.Lines, scheduler.Line{State: scheduler.LineDone}, tc.line)
		})
	}
}

func endInCut(t *testing.T, open func() (scheduler.Records, EndInFunc)) {
	reasons := make([]scheduler.Reason, 40)
	for i := range reasons {
		reasons[i] = scheduler.Reason{Code: "missing", Subject: fmt.Sprintf("connection:%d", i)}
	}
	facts := json.RawMessage(`{"k":"` + strings.Repeat("a", 3072-len(`{"k":""}`)) + `"}`)
	r, endIn := open()
	f := &fixture{t: t, r: r}
	rec := f.running()
	got := f.endIn(endIn, rec.ID, scheduler.End{Result: scheduler.ResultPartial, Reasons: reasons, Facts: facts})
	want := append(slices.Clone(reasons[:31]), scheduler.Reason{Code: scheduler.ReasonFactsTooLarge})
	if got.End == nil {
		t.Fatal("no end stored")
	}
	if len(got.End.Facts) != 0 || !slices.Equal(got.End.Reasons, want) {
		t.Errorf("end with %d bytes of facts and reasons %v, want no facts and the 31 most severe reasons then facts_too_large",
			len(got.End.Facts), got.End.Reasons)
	}
}

func endInKept(t *testing.T, r scheduler.Records, endIn EndInFunc) {
	f := &fixture{t: t, r: r}
	rec := f.running(scheduler.Line{State: scheduler.LineDone}, scheduler.Line{State: scheduler.LineQueued})
	committed := f.endIn(endIn, rec.ID, scheduler.End{Result: scheduler.ResultOK})
	got := f.end(rec.ID, scheduler.End{Result: scheduler.ResultFailed})
	if got.End == nil || got.End.Result != scheduler.ResultPartial || !got.EndedAt.Equal(committed.EndedAt) {
		t.Errorf("End after EndIn answered %+v at %v; want the committed partial at %v", got.End, got.EndedAt, committed.EndedAt)
	}
	wantLines(t, f.get(rec.ID).Lines, scheduler.Line{State: scheduler.LineDone},
		scheduler.Line{State: scheduler.LineReview, Reason: scheduler.ReasonNotRun})
}

func endInRefused(t *testing.T, r scheduler.Records, endIn EndInFunc) {
	f := &fixture{t: t, r: r}
	queued := f.insert(f.record("grab", ""), nil)
	if err := endIn(t.Context(), queued.ID, scheduler.End{Result: scheduler.ResultOK}); err == nil {
		t.Error("EndIn(a queued record) = nil, want an error")
	}
	if got := f.get(queued.ID); got.State != scheduler.StateQueued || got.End != nil || got.Rev != queued.Rev {
		t.Errorf("the refused queued record: state %q end %+v rev %d; want it as inserted", got.State, got.End, got.Rev)
	}

	ended := f.ran("grab", "", scheduler.ResultFailed)
	if err := endIn(t.Context(), ended.ID, scheduler.End{Result: scheduler.ResultOK}); err == nil {
		t.Error("EndIn(an ended record) = nil, want an error")
	}
	if got := f.get(ended.ID); got.End == nil || got.End.Result != scheduler.ResultFailed || got.Rev != ended.Rev {
		t.Errorf("the refused ended record: end %+v rev %d; want failed at rev %d", got.End, got.Rev, ended.Rev)
	}
}
