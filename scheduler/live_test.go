package scheduler_test

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/scheduler/v2"
)

// moves keeps the time of each Observer.Moved call and the Live counts of
// one job at that call.
type moves struct {
	ex   *scheduler.Executor[payload]
	at   []time.Time
	live []scheduler.LiveJob
	job  scheduler.ID
	mu   sync.Mutex
}

func (m *moves) observer() scheduler.Observer {
	return scheduler.Observer{Moved: func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.at = append(m.at, time.Now())
		m.live = append(m.live, m.ex.Live()[m.job])
	}}
}

func (m *moves) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.at)
}

func (m *moves) since(n int) (at []time.Time, live []scheduler.LiveJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]time.Time(nil), m.at[n:]...), append([]scheduler.LiveJob(nil), m.live[n:]...)
}

// wantLive fails the test unless Live answers want for id.
func (r *rig) wantLive(t *testing.T, id scheduler.ID, want scheduler.LiveJob) {
	t.Helper()
	got, ok := r.ex.Live()[id]
	if !ok {
		t.Errorf("Live()[%s] absent, want %+v", id, want)
		return
	}
	if got.Position != want.Position || got.Done != want.Done || got.Total != want.Total || !got.Until.Equal(want.Until) {
		t.Errorf("Live()[%s] = %+v, want %+v", id, got, want)
	}
}

func TestProgressIsLive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		report, gate := make(chan struct{}), make(chan struct{})
		k := spec("sweep", "")
		k.Run = func(ctx context.Context, run *scheduler.Run[payload]) scheduler.End {
			<-report
			run.Progress(2, 4)
			select {
			case <-gate:
			case <-ctx.Done():
			}
			return scheduler.End{Result: scheduler.ResultOK}
		}
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}})
		s := r.start(t, timer("sweep", "", payload{}))
		synctest.Wait()
		r.wantLive(t, s.Job, scheduler.LiveJob{})
		close(report)
		synctest.Wait()
		r.wantLive(t, s.Job, scheduler.LiveJob{Done: 2, Total: 4})
		if rec := r.rec(t, s.Job); rec.State != scheduler.StateRunning {
			t.Errorf("record state %s while it reports progress, want running", rec.State)
		}
		close(gate)
		synctest.Wait()
		if l, ok := r.ex.Live()[s.Job]; ok {
			t.Errorf("Live()[%s] = %+v after its end, want absent", s.Job, l)
		}
	})
}

func TestLiveQueuePositions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &moves{}
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{spec("quick", "passes")}, Observer: m.observer()})
		m.ex = r.ex
		gateA, gateD := make(chan struct{}), make(chan struct{})
		a := r.start(t, timer("quick", "a", payload{Gate: gateA}))
		synctest.Wait()
		b := r.start(t, timer("quick", "b", payload{}))
		c := r.start(t, timer("quick", "c", payload{}))
		synctest.Wait()
		if b.Position != 0 || c.Position != 1 {
			t.Fatalf("Started positions b %d c %d, want 0 and 1", b.Position, c.Position)
		}
		r.wantLive(t, a.Job, scheduler.LiveJob{})
		r.wantLive(t, b.Job, scheduler.LiveJob{Position: 0})
		r.wantLive(t, c.Job, scheduler.LiveJob{Position: 1})

		time.Sleep(2 * time.Second)
		synctest.Wait()
		before := m.count()
		d := r.start(t, api("quick", "d", "alice", payload{Gate: gateD}))
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if d.Position != 0 {
			t.Errorf("Started position of a person's job behind two automatic ones = %d, want 0", d.Position)
		}
		r.wantLive(t, d.Job, scheduler.LiveJob{Position: 0})
		r.wantLive(t, b.Job, scheduler.LiveJob{Position: 1})
		r.wantLive(t, c.Job, scheduler.LiveJob{Position: 2})
		if m.count() == before {
			t.Error("Moved not called after a person's job moved two queued jobs back")
		}

		before = m.count()
		close(gateA)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if l, ok := r.ex.Live()[a.Job]; ok {
			t.Errorf("Live()[a] = %+v after its end, want absent", l)
		}
		r.wantLive(t, d.Job, scheduler.LiveJob{})
		r.wantLive(t, b.Job, scheduler.LiveJob{Position: 0})
		r.wantLive(t, c.Job, scheduler.LiveJob{Position: 1})
		if m.count() == before {
			t.Error("Moved not called after the slot passed to the next job")
		}

		close(gateD)
		synctest.Wait()
		if l := r.ex.Live(); len(l) != 0 {
			t.Errorf("Live() = %+v once every job ended, want empty", l)
		}
	})
}

func TestLiveYieldUntil(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &moves{}
		after := make(chan struct{})
		y := &yielder{got: &order{}, until: func(now time.Time) time.Time { return now.Add(10 * time.Minute) }, gateAfter: after}
		r := newRig(t, scheduler.Config[payload]{
			Kinds:    []scheduler.KindSpec[payload]{y.spec("publish", "passes"), spec("quick", "passes")},
			Observer: m.observer(),
		})
		m.ex = r.ex
		t0 := time.Now()
		p := r.start(t, timer("publish", "", payload{}))
		time.Sleep(2 * time.Second)
		synctest.Wait()
		r.wantLive(t, p.Job, scheduler.LiveJob{Until: t0.Add(10 * time.Minute)})
		if m.count() == 0 {
			t.Error("Moved not called after the job yielded")
		}

		gate := make(chan struct{})
		r.start(t, timer("quick", "holder", payload{Gate: gate}))
		synctest.Wait()
		sleepUntil(t0.Add(time.Minute))
		before := m.count()
		if !r.ex.Wake(p.Job) {
			t.Fatal("Wake of the yielded job = false, want true")
		}
		woke := time.Now()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		r.wantLive(t, p.Job, scheduler.LiveJob{Until: woke})
		if m.count() == before {
			t.Error("Moved not called after Wake")
		}

		before = m.count()
		close(gate)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		r.wantLive(t, p.Job, scheduler.LiveJob{})
		if m.count() == before {
			t.Error("Moved not called after the yielded job took its slot back")
		}
		close(after)
		synctest.Wait()
		r.wantEnd(t, p.Job, scheduler.ResultOK)
	})
}

func TestMovedAtMostOncePerSecond(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &moves{}
		release, gate := make(chan struct{}), make(chan struct{})
		k := spec("sweep", "")
		k.Run = func(ctx context.Context, run *scheduler.Run[payload]) scheduler.End {
			<-release
			for i := 1; i <= 100; i++ {
				run.Progress(i, 100)
				time.Sleep(10 * time.Millisecond)
			}
			select {
			case <-gate:
			case <-ctx.Done():
			}
			return scheduler.End{Result: scheduler.ResultOK}
		}
		r := newRig(t, scheduler.Config[payload]{Kinds: []scheduler.KindSpec[payload]{k}, Observer: m.observer()})
		m.ex = r.ex
		s := r.start(t, timer("sweep", "", payload{}))
		m.mu.Lock()
		m.job = s.Job
		m.mu.Unlock()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		n := m.count()

		t0 := time.Now()
		close(release)
		time.Sleep(5 * time.Second)
		synctest.Wait()
		at, live := m.since(n)
		if len(at) != 2 {
			t.Fatalf("Moved called %d times for 100 counts in one second, at %v, want 2: one at the first and one trailing", len(at), at)
		}
		if !at[0].Equal(t0) || !at[1].Equal(t0.Add(time.Second)) {
			t.Errorf("Moved at %v and %v, want at the first count %v and one second later", at[0], at[1], t0)
		}
		if live[0].Done != 1 || live[1].Done != 100 || live[1].Total != 100 {
			t.Errorf("Live at the calls = %+v and %+v, want done 1 then the last count 100 of 100", live[0], live[1])
		}
		close(gate)
		synctest.Wait()
	})
}
