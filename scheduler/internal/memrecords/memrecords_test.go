package memrecords_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/cplieger/go/scheduler/v2"
	"github.com/cplieger/go/scheduler/v2/internal/memrecords"
	"github.com/cplieger/go/scheduler/v2/schedulertest"
)

func TestRecordsContract(t *testing.T) {
	schedulertest.Records(t, func() scheduler.Records { return memrecords.New() })
}

func TestEndInContract(t *testing.T) {
	schedulertest.EndIn(t, func() (scheduler.Records, schedulertest.EndInFunc) {
		s := memrecords.New()
		s.Logger(slog.New(slog.DiscardHandler))
		return s, func(_ context.Context, id scheduler.ID, e scheduler.End) error { return s.EndIn(id, e) }
	})
}

func id(n int) scheduler.ID { return scheduler.ID(fmt.Sprintf("%032x", n)) }

func insert(t *testing.T, s *memrecords.Store, n int) scheduler.Record {
	t.Helper()
	r, _, err := s.Insert(t.Context(), scheduler.Record{ID: id(n), Kind: "scan", State: scheduler.StateQueued, Rev: 1}, nil)
	if err != nil {
		t.Fatalf("Insert(%d) = %v", n, err)
	}
	return r
}

func start(t *testing.T, s *memrecords.Store, r scheduler.Record, lines ...scheduler.Line) scheduler.Record {
	t.Helper()
	r.State, r.StartedAt, r.Lines = scheduler.StateRunning, time.Now(), lines
	if err := s.Put(t.Context(), r); err != nil {
		t.Fatalf("Put = %v", err)
	}
	return r
}

func TestEndIn(t *testing.T) {
	s := memrecords.New()
	queued := insert(t, s, 1)
	if err := s.EndIn(queued.ID, scheduler.End{Result: scheduler.ResultOK}); !errors.Is(err, memrecords.ErrNotRunning) {
		t.Errorf("EndIn(a queued record) = %v, want ErrNotRunning", err)
	}
	if err := s.EndIn(id(99), scheduler.End{Result: scheduler.ResultOK}); !errors.Is(err, memrecords.ErrNotRunning) {
		t.Errorf("EndIn(an unknown id) = %v, want ErrNotRunning", err)
	}

	running := start(t, s, insert(t, s, 2))
	if err := s.EndIn(running.ID, scheduler.End{Result: scheduler.ResultOK}); err != nil {
		t.Fatalf("EndIn(a running record) = %v", err)
	}
	got, err := s.End(t.Context(), running.ID, scheduler.End{Result: scheduler.ResultFailed})
	if err != nil || got.End == nil || got.End.Result != scheduler.ResultOK {
		t.Errorf("End after EndIn = %+v, %v; want the end written in the commit, ok", got.End, err)
	}
	if err := s.EndIn(running.ID, scheduler.End{Result: scheduler.ResultFailed}); !errors.Is(err, memrecords.ErrNotRunning) {
		t.Errorf("EndIn(an ended record) = %v, want ErrNotRunning", err)
	}
	if got, _, _ := s.Get(t.Context(), queued.ID); got.State != scheduler.StateQueued || got.End != nil {
		t.Errorf("the refused record = state %q end %+v; want it queued, unended", got.State, got.End)
	}
}

func TestCrashAndReopen(t *testing.T) {
	s := memrecords.New()
	queued := insert(t, s, 1)
	running := start(t, s, insert(t, s, 2),
		scheduler.Line{State: scheduler.LineDone},
		scheduler.Line{State: scheduler.LineRunning},
		scheduler.Line{State: scheduler.LineQueued},
		scheduler.Line{State: scheduler.LineSkipped, Reason: "nothing_found"},
	)
	ended := start(t, s, insert(t, s, 3))
	if _, err := s.End(t.Context(), ended.ID, scheduler.End{Result: scheduler.ResultOK}); err != nil {
		t.Fatalf("End = %v", err)
	}

	c := s.Crash()
	c.Reopen()

	for name, rid := range map[string]scheduler.ID{"queued": queued.ID, "running": running.ID} {
		got, ok, err := c.Get(t.Context(), rid)
		if err != nil || !ok || got.State != scheduler.StateEnded || got.End == nil || got.End.Result != scheduler.ResultInterrupted {
			t.Errorf("the %s record after Reopen = state %q end %+v; want ended interrupted", name, got.State, got.End)
		}
	}
	got, _, _ := c.Get(t.Context(), running.ID)
	want := []scheduler.Line{
		{State: scheduler.LineDone},
		{State: scheduler.LineReview, Reason: scheduler.ReasonInterrupted},
		{State: scheduler.LineReview, Reason: scheduler.ReasonInterrupted},
		{State: scheduler.LineSkipped, Reason: "nothing_found"},
	}
	if len(got.Lines) != len(want) {
		t.Fatalf("lines after Reopen = %+v, want %+v", got.Lines, want)
	}
	for i := range want {
		if got.Lines[i].State != want[i].State || got.Lines[i].Reason != want[i].Reason {
			t.Errorf("line %d after Reopen = %+v, want %+v", i, got.Lines[i], want[i])
		}
	}
	if got, _, _ := c.Get(t.Context(), ended.ID); got.End == nil || got.End.Result != scheduler.ResultOK {
		t.Errorf("the ended record after Reopen = %+v, want its own ok end", got.End)
	}
	if got, _, _ := s.Get(t.Context(), running.ID); got.State != scheduler.StateRunning {
		t.Errorf("the store Crash copied = state %q, want it still running", got.State)
	}
}

func TestCapacity(t *testing.T) {
	s := memrecords.New()
	s.Capacity(1)
	key := &scheduler.Key{Credential: "c", Row: "r", Key: "k"}
	first, _, err := s.Insert(t.Context(), scheduler.Record{ID: id(1), State: scheduler.StateQueued}, key)
	if err != nil {
		t.Fatalf("Insert under the bound = %v", err)
	}
	if _, _, err := s.Insert(t.Context(), scheduler.Record{ID: id(2), State: scheduler.StateQueued}, nil); !errors.Is(err, scheduler.ErrCapacity) {
		t.Errorf("Insert at the bound = %v, want ErrCapacity", err)
	}
	if got, replayed, err := s.Insert(t.Context(), scheduler.Record{ID: id(3)}, key); err != nil || !replayed || got.ID != first.ID {
		t.Errorf("a keyed resend at the bound = %s replayed %v, %v; want the stored record", got.ID, replayed, err)
	}
	if n := len(s.All()); n != 1 {
		t.Errorf("records at the bound = %d, want 1", n)
	}
	s.Capacity(0)
	insert(t, s, 4)
}

func TestFail(t *testing.T) {
	boom := errors.New("boom")
	s := memrecords.New()
	r := insert(t, s, 1)
	// Each call writes nothing when it succeeds, so the store must end as it began.
	for op, call := range map[memrecords.Op]func() error{
		memrecords.OpInsert: func() error {
			_, _, err := s.Insert(t.Context(), scheduler.Record{ID: r.ID}, nil)
			return err
		},
		memrecords.OpPut: func() error { return s.Put(t.Context(), scheduler.Record{ID: id(99)}) },
		memrecords.OpEnd: func() error {
			_, err := s.End(t.Context(), id(99), scheduler.End{Result: scheduler.ResultOK})
			return err
		},
		memrecords.OpGet: func() error {
			_, _, err := s.Get(t.Context(), r.ID)
			return err
		},
		memrecords.OpByKey: func() error {
			_, _, err := s.ByKey(t.Context(), "c", "r", "k")
			return err
		},
		memrecords.OpDismiss: func() error { return s.Dismiss(t.Context(), r.ID, 0) },
		memrecords.OpLive: func() error {
			_, err := s.Live(t.Context(), time.Now())
			return err
		},
		memrecords.OpHistory: func() error {
			_, _, err := s.History(t.Context(), "", "", 1)
			return err
		},
		memrecords.OpLastEnds: func() error {
			_, err := s.LastEnds(t.Context())
			return err
		},
	} {
		s.Fail(op, boom)
		if err := call(); !errors.Is(err, boom) {
			t.Errorf("%s with a failure set = %v, want it", op, err)
		}
		s.Fail(op, nil)
		before := s.Count(op)
		_ = call()
		if s.Count(op) != before+1 {
			t.Errorf("Count(%s) did not move on a call", op)
		}
	}
	if got := s.All(); len(got) != 1 || got[0].State != scheduler.StateQueued {
		t.Errorf("after the calls the store holds %+v, want the one queued record", got)
	}
}

func TestPutUnknownAndCursor(t *testing.T) {
	s := memrecords.New()
	if err := s.Put(t.Context(), scheduler.Record{ID: id(1)}); !errors.Is(err, scheduler.ErrUnknownJob) {
		t.Errorf("Put(an unknown id) = %v, want ErrUnknownJob", err)
	}
	if _, _, err := s.History(t.Context(), "", "nope", 1); !errors.Is(err, memrecords.ErrCursor) {
		t.Errorf("History(an unknown cursor) = %v, want ErrCursor", err)
	}
}
