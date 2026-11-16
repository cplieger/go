package fixture

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/sse/v2"
	"github.com/cplieger/go/sse/v2/feedwire"
	"github.com/cplieger/go/sse/v2/ssetest"
)

const feedWire = "0123456789abcdef"

// newFeedSession creates a session with a logged store (runs, budgets) at rev
// 100 and an unlogged live (queue) at rev 7, and returns its id.
func newFeedSession(t *testing.T, base string) string {
	t.Helper()
	var resp sessionResponse
	body := `{"wire":"` + feedWire + `","sources":[` +
		`{"name":"store","log":true,"kinds":["runs","budgets"],"rev":100},` +
		`{"name":"live","kinds":["queue"],"rev":7},` +
		`{"name":"accounts","log":true,"kinds":["users"],"pending":true}]}`
	if status := post(t, base+"/control/feed/sessions", body, nil, &resp); status != http.StatusOK || resp.ID == "" || resp.Wire != feedWire {
		t.Fatalf("create session = %d %+v, want 200 with an id and the wire", status, resp)
	}
	return resp.ID
}

func feedControl(t *testing.T, base, id, verb, body string, out any) {
	t.Helper()
	status := post(t, base+"/control/feed/"+id+"/"+verb, body, nil, out)
	if status != http.StatusNoContent && status != http.StatusOK {
		t.Fatalf("POST /control/feed/%s/%s %s = %d, want 204 or 200", id, verb, body, status)
	}
}

func feedState(t *testing.T, base, id string) feedStateResponse {
	t.Helper()
	resp := get(t, base+"/control/feed/"+id+"/state", nil)
	defer resp.Body.Close()
	var st feedStateResponse
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// openFeed connects to the session's stream, consumes the retry field and the
// hello, and returns the frames that follow.
func openFeed(t *testing.T, base, id string, header http.Header) (*frameFeed, sse.Hello) {
	t.Helper()
	resp := get(t, base+"/feed/"+id+"/events", header)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /feed/%s/events = %d, want 200", id, resp.StatusCode)
	}
	t.Cleanup(func() { resp.Body.Close() })
	stream := ssetest.NewFrameReader(resp.Body)
	frames, err := stream.Read(2)
	if err != nil || frames[1].Event != "sse:hello" {
		t.Fatalf("handshake = %+v (%v), want the retry field and the hello", frames, err)
	}
	var hello sse.Hello
	if err := json.Unmarshal([]byte(frames[1].Data), &hello); err != nil {
		t.Fatal(err)
	}
	return feedFrames(stream), hello
}

func nextFrame(t *testing.T, ff *frameFeed, event string) ssetest.Frame {
	t.Helper()
	for {
		fr, err := ff.next(pollTimeout)
		if err != nil {
			t.Fatalf("waiting for a %s frame: %v", event, err)
		}
		if fr.Event == event {
			return fr
		}
		if fr.Event != "filler" {
			t.Fatalf("frame = %+v, want %s", fr, event)
		}
	}
}

func TestFeedListeningFirstAfterHello(t *testing.T) {
	_, base := fixtureServer(t)
	id := newFeedSession(t, base)
	feedControl(t, base, id, "commit", `{"source":"store","rows":[{"kind":"runs","key":"r1","value":"a"}]}`, nil)
	ff, _ := openFeed(t, base, id, nil)
	fr, err := ff.next(pollTimeout)
	if err != nil || fr.Event != "listening" || fr.ID != "" {
		t.Fatalf("first frame after the hello = %+v (%v), want an id-less listening frame", fr, err)
	}
	l, err := feedwire.DecodeListening([]byte(fr.Data))
	if err != nil {
		t.Fatal(err)
	}
	st := feedState(t, base, id)
	if l.Wire != feedWire || len(l.Sources) != 2 || l.Sources[0].Source != "store" || l.Sources[1].Source != "live" {
		t.Fatalf("listening = %+v, want the wire and the two started sources", l)
	}
	if l.Sources[0].Epoch != st.Sources[0].Epoch || l.Sources[0].Floors["runs"] != 101 || len(l.Sources[0].Floors) != 1 || len(l.Sources[1].Floors) != 0 {
		t.Errorf("store = %+v, want epoch %s and only runs at 101", l.Sources, st.Sources[0].Epoch)
	}
}

func TestFeedCommitPublishesChanged(t *testing.T) {
	_, base := fixtureServer(t)
	id := newFeedSession(t, base)
	ff, _ := openFeed(t, base, id, nil)
	nextFrame(t, ff, "listening")
	var resp commitResponse
	feedControl(t, base, id, "commit", `{"source":"store","rows":[{"kind":"runs","key":"r1","value":"a"},{"kind":"budgets","key":"b","value":"1"}]}`, &resp)
	fr := nextFrame(t, ff, "changed")
	c, err := feedwire.DecodeChanged([]byte(fr.Data))
	if err != nil || fr.ID == "" {
		t.Fatalf("changed frame %+v: %v, want a decodable frame with an id", fr, err)
	}
	if c.Source != "store" || c.Epoch != resp.Epoch || c.Rev != 101 || !slices.Equal(c.Kinds, []string{"budgets", "runs"}) {
		t.Errorf("changed = %+v, want store at 101 naming budgets and runs", c)
	}
	feedControl(t, base, id, "bump", `{"source":"live","kinds":["queue"],"count":3}`, nil)
	for want := uint64(8); want <= 10; want++ {
		c, err := feedwire.DecodeChanged([]byte(nextFrame(t, ff, "changed").Data))
		if err != nil || c.Source != "live" || c.Rev != want {
			t.Errorf("bump frame = %+v (%v), want live at %d", c, err, want)
		}
	}
}

func getJSON(t *testing.T, url string, out any) (int, http.Header) {
	t.Helper()
	resp := get(t, url, nil)
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("GET %s: %v in %q", url, err, raw)
		}
	}
	return resp.StatusCode, resp.Header
}

func TestFeedChangesAnswersSince(t *testing.T) {
	_, base := fixtureServer(t)
	id := newFeedSession(t, base)
	feedControl(t, base, id, "commit", `{"source":"store","rows":[{"kind":"runs","key":"r1","value":"a"}]}`, nil)
	feedControl(t, base, id, "commit", `{"source":"store","rows":[{"kind":"runs","key":"r2","value":"b"}]}`, nil)
	var resp commitResponse
	feedControl(t, base, id, "commit", `{"source":"store","rows":[{"kind":"runs","key":"r1","removed":true},{"kind":"budgets","key":"x","value":"1"}]}`, &resp)
	changes := func(epoch, since, kinds string) (int, changesAnswer) {
		var a changesAnswer
		status, _ := getJSON(t, base+"/feed/"+id+"/changes?source=store&epoch="+epoch+"&since="+since+"&kinds="+kinds, &a)
		return status, a
	}
	t.Run("since 101 holds r2 and r1's removal", func(t *testing.T) {
		status, a := changes(resp.Epoch, "101", "runs")
		if status != http.StatusOK || a.Full || a.Rev != 103 || len(a.Upserts["runs"]) != 1 || a.Upserts["runs"][0] != (feedRow{Key: "r2", Value: "b"}) ||
			!slices.Equal(a.Removed["runs"], []string{"r1"}) || len(a.Upserts["budgets"]) != 0 {
			t.Errorf("answer = %d %+v, want r2 upserted, r1 removed, nothing of budgets, rev 103", status, a)
		}
	})
	t.Run("full below the log, above the head or on another epoch", func(t *testing.T) {
		for _, q := range [][2]string{{resp.Epoch, "99"}, {resp.Epoch, "104"}, {strings.Repeat("a", 32), "101"}} {
			if status, a := changes(q[0], q[1], "runs"); status != http.StatusOK || !a.Full || a.Epoch != resp.Epoch {
				t.Errorf("answer for %v = %d %+v, want full on the current epoch", q, status, a)
			}
		}
	})
	t.Run("maintenance then rows", func(t *testing.T) {
		feedControl(t, base, id, "changes-fail", `{"count":1}`, nil)
		if status, _ := changes(resp.Epoch, "102", "budgets"); status != http.StatusServiceUnavailable {
			t.Errorf("first answer = %d, want 503", status)
		}
		if status, a := changes(resp.Epoch, "102", "budgets"); status != http.StatusOK || len(a.Upserts["budgets"]) != 1 {
			t.Errorf("second answer = %d %+v, want budgets x", status, a)
		}
	})
	t.Run("an unlogged source or a foreign kind is refused", func(t *testing.T) {
		status, _ := getJSON(t, base+"/feed/"+id+"/changes?source=live&epoch="+resp.Epoch+"&since=7&kinds=queue", nil)
		if status != http.StatusBadRequest {
			t.Errorf("unlogged = %d, want 400", status)
		}
		if status, _ := changes(resp.Epoch, "101", "queue"); status != http.StatusBadRequest {
			t.Errorf("foreign kind = %d, want 400", status)
		}
	})
	answers := []string{}
	for _, q := range feedState(t, base, id).Queries {
		answers = append(answers, q.Answer)
	}
	if want := []string{"rows", "full", "full", "full", "maintenance", "rows", "bad_query", "bad_query"}; !slices.Equal(answers, want) {
		t.Errorf("recorded answers = %v, want %v", answers, want)
	}
}

func TestFeedPositionHeader(t *testing.T) {
	_, base := fixtureServer(t)
	id := newFeedSession(t, base)
	var resp commitResponse
	feedControl(t, base, id, "commit", `{"source":"store","rows":[{"kind":"runs","key":"r1","value":"a"}]}`, &resp)
	var read readResponse
	status, header := getJSON(t, base+"/feed/"+id+"/read?kind=runs", &read)
	ps, err := feedwire.ParsePosition(header.Get("Feed-Position"))
	if status != http.StatusOK || err != nil || len(ps) != 1 || ps[0] != (feedwire.Position{Source: "store", Epoch: resp.Epoch, Rev: 101}) {
		t.Errorf("read = %d, Feed-Position %v (%v), want store at 101", status, ps, err)
	}
	if len(read.Rows) != 1 || read.Rows[0] != (feedRow{Key: "r1", Value: "a"}) {
		t.Errorf("rows = %+v, want r1", read.Rows)
	}
	if _, header := getJSON(t, base+"/feed/"+id+"/read?kind=users", nil); header.Get("Feed-Position") != "" {
		t.Errorf("a pending source's read carries Feed-Position %q, want none", header.Get("Feed-Position"))
	}
	feedControl(t, base, id, "read-fail", `{"count":1,"mode":"unavailable"}`, nil)
	if status, _ := getJSON(t, base+"/feed/"+id+"/read?kind=runs", nil); status != http.StatusServiceUnavailable {
		t.Errorf("failed read = %d, want 503", status)
	}
	if got := feedState(t, base, id).Reads; got["runs"] != 2 || got["users"] != 1 {
		t.Errorf("reads = %v, want runs 2 and users 1", got)
	}
}

func TestFeedReopenPublishesListeningWithID(t *testing.T) {
	_, base := fixtureServer(t)
	id := newFeedSession(t, base)
	ff, _ := openFeed(t, base, id, nil)
	before, err := feedwire.DecodeListening([]byte(nextFrame(t, ff, "listening").Data))
	if err != nil {
		t.Fatal(err)
	}
	var resp commitResponse
	feedControl(t, base, id, "reopen", `{"source":"store","rev":40}`, &resp)
	fr := nextFrame(t, ff, "listening")
	l, err := feedwire.DecodeListening([]byte(fr.Data))
	if err != nil || fr.ID == "" || l.Wire != "" || len(l.Sources) != 1 {
		t.Fatalf("reopen frame = %+v (%v), want one source, an id and no wire", fr, err)
	}
	if s := l.Sources[0]; s.Source != "store" || s.Epoch == before.Sources[0].Epoch || s.Epoch != resp.Epoch || resp.Rev != 40 || len(s.Floors) != 0 {
		t.Errorf("reopened = %+v (%+v), want store on a new epoch at 40 with no floor", s, resp)
	}
	feedControl(t, base, id, "start-source", `{"source":"accounts"}`, nil)
	if l, err := feedwire.DecodeListening([]byte(nextFrame(t, ff, "listening").Data)); err != nil || l.Sources[0].Source != "accounts" {
		t.Errorf("start frame = %+v (%v), want accounts", l, err)
	}
}

func TestFeedSessionsAreIsolated(t *testing.T) {
	_, base := fixtureServer(t)
	a, b := newFeedSession(t, base), newFeedSession(t, base)
	ffb, _ := openFeed(t, base, b, nil)
	nextFrame(t, ffb, "listening")
	feedControl(t, base, a, "commit", `{"source":"store","rows":[{"kind":"runs","key":"r1","value":"a"}]}`, nil)
	if fr, err := ffb.next(200 * time.Millisecond); err == nil {
		t.Errorf("session b received %+v from a's commit", fr)
	}
	sa, sb := feedState(t, base, a), feedState(t, base, b)
	if sa.Sources[0].Epoch == sb.Sources[0].Epoch || sa.Sources[0].Rev != 101 || sb.Sources[0].Rev != 100 || sb.Connected != 1 || sa.Connects != 0 {
		t.Errorf("states a %+v, b %+v, want separate epochs and revisions, b connected once", sa, sb)
	}
	if status := post(t, base+"/control/feed/nope/commit", `{}`, nil, nil); status != http.StatusNotFound {
		t.Errorf("unknown session = %d, want 404", status)
	}
}

func TestFeedDropAndRestart(t *testing.T) {
	_, base := fixtureServer(t)
	id := newFeedSession(t, base)
	ff, _ := openFeed(t, base, id, nil)
	nextFrame(t, ff, "listening")
	feedControl(t, base, id, "commit", `{"source":"store","rows":[{"kind":"runs","key":"r1","value":"a"}]}`, nil)
	cursor := nextFrame(t, ff, "changed").ID
	reconnect := func() sse.Hello {
		t.Helper()
		_, hello := openFeed(t, base, id, http.Header{"Last-Event-ID": {cursor}})
		return hello
	}
	t.Run("with resume", func(t *testing.T) {
		feedControl(t, base, id, "drop", `{"resume":true}`, nil)
		if _, err := ff.next(pollTimeout); err == nil {
			t.Fatal("the stream outlived the drop")
		}
		if hello := reconnect(); hello.Verdict != sse.VerdictResumed {
			t.Errorf("verdict = %s, want resumed", hello.Verdict)
		}
	})
	t.Run("without resume", func(t *testing.T) {
		feedControl(t, base, id, "drop", `{"resume":false}`, nil)
		if hello := reconnect(); hello.Verdict != sse.VerdictGapFloor {
			t.Errorf("verdict = %s, want gap_floor", hello.Verdict)
		}
	})
	t.Run("restart", func(t *testing.T) {
		before := feedState(t, base, id).Sources[0]
		feedControl(t, base, id, "restart", ``, nil)
		after := feedState(t, base, id).Sources[0]
		if hello := reconnect(); hello.Verdict != sse.VerdictEpochChanged || after.Epoch == before.Epoch || after.Rev != before.Rev {
			t.Errorf("verdict = %s, source %+v after %+v, want epoch_changed and a new epoch at the same rev", hello.Verdict, after, before)
		}
	})
	t.Run("hold keeps a stream off the hub until release", func(t *testing.T) {
		feedControl(t, base, id, "hold", ``, nil)
		joined := make(chan struct{})
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/feed/"+id+"/events", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			defer close(joined)
			if resp, err := testClient.Do(req); err == nil {
				resp.Body.Close()
			}
		}()
		time.Sleep(100 * time.Millisecond)
		select {
		case <-joined:
			t.Fatal("the stream joined while held")
		default:
		}
		feedControl(t, base, id, "release", ``, nil)
		<-joined
		if got := feedState(t, base, id).Connects; got < 1 {
			t.Errorf("connects = %d after the release, want the held stream counted", got)
		}
	})
}
