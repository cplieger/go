package httpwire

// CheckAnswer is one source's answer to an open check, a keyed re-read of
// one item at a source it came from.
type CheckAnswer string

// The open check's answers.
const (
	CheckSame    CheckAnswer = "same"    // found, and the stored row already matches it
	CheckUpdated CheckAnswer = "updated" // found, and the item changed
	CheckGone    CheckAnswer = "gone"    // the source no longer has the item
	CheckError   CheckAnswer = "error"   // the read failed; Code says why
)

// NotStored is why an updated or gone answer was not stored.
type NotStored string

// The reasons an answer was not stored.
const (
	NotStoredSuperseded NotStored = "superseded"       // the stored row moved while the check read
	NotStoredUnwritable NotStored = "store_unwritable" // the store refused the write
)

// Checked is an open check's answer: one entry per source the item came
// from, each source named once.
type Checked[T any] struct {
	Sources []SourceCheck[T] `json:"sources"`
}

// SourceCheck is one source's answer. Which fields are set follows Answer:
//   - same: none of Row, Stored, NotStored, Code and Upstream;
//   - updated: Row (as stored when Stored, else as the source answered it),
//     and exactly one of Stored and NotStored;
//   - gone: exactly one of Stored and NotStored, and no Row;
//   - error: Code, and Upstream when the source answered; no Row, Stored
//     or NotStored.
type SourceCheck[T any] struct {
	Row      *T        `json:"row,omitempty"`
	Upstream *Upstream `json:"upstream,omitempty"`
	// Source is the app's name for the source, unique in its Checked.
	Source    string      `json:"source"`
	Answer    CheckAnswer `json:"answer"`
	NotStored NotStored   `json:"not_stored,omitempty"`
	// Code is the failure's code, in the code grammar.
	Code   string `json:"code,omitempty"`
	Stored bool   `json:"stored"`
}
