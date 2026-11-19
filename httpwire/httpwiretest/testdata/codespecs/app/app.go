// Package app is an app whose codes reach the wire only through httpwire's
// types.
package app

import (
	"errors"

	"github.com/cplieger/go/httpwire"
)

var errTaken = errors.New("taken")

var owner = &httpwire.Owner{
	Name:      "app",
	Sentinels: map[string]error{"errTaken": errTaken},
	Codes:     []httpwire.OwnerCode{{Err: errTaken, Code: "name_taken"}},
}

func write(locked bool) error {
	if locked {
		return &httpwire.Error{Code: "note_locked", Message: "the note is locked"}
	}
	return &httpwire.Error{Code: "note_refused", Message: "the note is refused"}
}

var _, _ = owner, write
