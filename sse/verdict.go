package sse

// Verdict is the hello's diagnostic account of how a presented cursor was
// judged. Clients branch on Hello.Resumed only; the verdict feeds logs and
// counters.
type Verdict string

// The seven verdicts. The three gap variants are kept distinct so a counter can
// tell a retention problem from a reply-cap problem from a client defect.
const (
	VerdictFresh         Verdict = "fresh"
	VerdictResumed       Verdict = "resumed"
	VerdictGapFloor      Verdict = "gap_floor"
	VerdictGapBudget     Verdict = "gap_budget"
	VerdictGapAhead      Verdict = "gap_ahead"
	VerdictEpochChanged  Verdict = "epoch_changed"
	VerdictCursorInvalid Verdict = "cursor_invalid"
)

// Hello is the handshake frame's payload, written once per connection right
// after the retry: field and handed to the WithOnConnect hook unchanged.
// Floor and Head are decimal strings on the wire because the peer is a
// JavaScript client.
type Hello struct {
	Epoch string `json:"epoch"`
	// Conn names a connection served with WithRequestScope to ScopeHandler,
	// and is empty otherwise. It is a capability: a WithOnConnect hook may
	// read it, but must not log it or send it to a third party.
	Conn           string  `json:"conn,omitempty"`
	Verdict        Verdict `json:"verdict"`
	KeepaliveEvent string  `json:"keepalive_event"`
	Floor          uint64  `json:"floor,string"`
	Head           uint64  `json:"head,string"`
	Wire           int     `json:"wire"`
	KeepaliveMS    int     `json:"keepalive_ms"`
	Resumed        bool    `json:"resumed"`
}

// resolve judges a presented cursor against the ring snapshot taken under the
// hub lock. The gap causes are evaluated ahead, floor, budget, so a connection
// reports the first reason it could not be resumed.
// Cursor and verdict shape follow Centrifugo's recovery handshake.
//
// matched counts the ring entries above the cursor that the connection's
// scope admits; the reply cap judges that count, while the floor stays
// unfiltered, because an evicted entry may have been in scope.
func resolve(cur Cursor, present bool, epoch string, floor, head uint64, n, replyCap int, matched func() int) (Verdict, bool) {
	switch {
	case !present:
		return VerdictFresh, false
	case cur.Epoch == "":
		return VerdictCursorInvalid, false
	case cur.Epoch != epoch:
		return VerdictEpochChanged, false
	}
	since := cur.Offset
	switch {
	case since > head:
		return VerdictGapAhead, false
	case since == head:
		return VerdictResumed, true
	case n == 0 || since+1 < floor:
		return VerdictGapFloor, false
	case matched() > replyCap:
		return VerdictGapBudget, false
	}
	return VerdictResumed, true
}
