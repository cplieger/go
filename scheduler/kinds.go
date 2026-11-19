package scheduler

import "encoding/json"

// Kind names one sort of job in code grammar ([a-z0-9_]+): "scan",
// "full_check", "refresh".
type Kind string

// Phase is a step of a kind's pipeline, in code grammar. The executor writes
// its own three; a kind names the others in KindSpec.Phases.
type Phase string

// The executor's phases.
const (
	PhaseQueued     Phase = "queued"
	PhaseStarted    Phase = "started"
	PhaseCommitting Phase = "committing" // reached by Run.Commit; Cancel refuses a job past it
)

// Trigger is who asked for a job.
type Trigger string

// The three entry points.
const (
	TriggerSchedule Trigger = "schedule" // the executor's own timer
	TriggerAPI      Trigger = "api"      // a product-table row: a session or an API key
	TriggerAdmin    Trigger = "admin"    // an admin-socket row: the CLI
)

// Class orders the jobs waiting for one slot.
type Class uint8

// The classes. A slot serves Interactive first, then a yielded job, then
// Automatic and Batch alternating while both wait.
const (
	Interactive Class = iota + 1 // a person's or a key's single job: first in line for its slot
	Automatic                    // a scheduled job
	Batch                        // one item of a batch job, whoever started the batch
)

// Result is how a job ended.
type Result string

// The results.
const (
	ResultOK      Result = "ok"
	ResultPartial Result = "partial" // finished, with part of the work not done
	ResultFailed  Result = "failed"
	// ResultCancelled is a cancel's end, or CredentialEnded's with reason
	// credential_ended.
	ResultCancelled Result = "cancelled"
	// ResultSkipped is either a Run that chose to do nothing, which is a
	// moving end, or a job that never started: a queued job ended covered, or
	// a request refused queue_full after its record was written. A
	// never-started skip moves no deadline.
	ResultSkipped     Result = "skipped"
	ResultInterrupted Result = "interrupted" // a restart, a hold or an import cut it short
)

// The reason codes the executor writes.
const (
	ReasonCovered         = "covered"          // Subject is the covering job's ID
	ReasonQueueFull       = "queue_full"       // the queue refused a request whose record was written
	ReasonCredentialEnded = "credential_ended" //nolint:gosec // G101: a reason code, not a credential
	ReasonFactsTooLarge   = "facts_too_large"  // the end's own Facts were dropped
	ReasonRunPanicked     = "run_panicked"
	ReasonInterrupted     = "interrupted"
	ReasonCancelled       = "cancelled"
	ReasonNotRun          = "not_run" // a batch line the Run never reached, at an end other than cancelled or interrupted
)

// Reason is one cause of an end, codes only. Subject is a scope-grammar key
// ("connection:3") or "".
type Reason struct{ Code, Subject string }

// End is how a job finished.
type End struct {
	Result  Result
	Reasons []Reason        // most severe first, at most 32
	Facts   json.RawMessage // optional: a JSON object of at most 2 KiB
}

// LineState is how far one item of a batch got.
type LineState string

// The line states. Queued and running are an item waiting or in progress; the
// other three are its end.
const (
	LineQueued  LineState = "queued"
	LineRunning LineState = "running"
	LineDone    LineState = "done"
	LineSkipped LineState = "skipped"
	LineReview  LineState = "review"
)

// Line is one item's state on a batch's record.
type Line struct {
	State  LineState
	Reason string          // the app's code for a skip or a review
	Facts  json.RawMessage // optional: a JSON object of at most 2 KiB
}

// maxScope is the longest scope, in bytes.
const maxScope = 128

// validCode reports whether s is in code grammar.
func validCode(s string) bool {
	return s != "" && grammar(s, false)
}

// validScope reports whether s is in scope grammar: "", or code grammar plus
// ':' and '/', at most maxScope bytes.
func validScope(s string) bool {
	return len(s) <= maxScope && grammar(s, true)
}

func grammar(s string, scope bool) bool {
	for i := range len(s) {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
		case scope && (c == ':' || c == '/'):
		default:
			return false
		}
	}
	return true
}
