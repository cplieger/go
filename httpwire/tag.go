package httpwire

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Tag is an IfMatch row's entity tag: a revision bound to what it lives in,
// so a revision an older file puts back never matches a tag read before.
type Tag struct {
	// Open is the token of what the revision lives in: a store file's open
	// (OpenToken of its epoch, read inside the transaction that reads or
	// checks the revision), or the first 8 bytes of a shared file's SHA-256.
	Open [8]byte
	Rev  uint64
}

// OpenToken answers the first 8 bytes of SHA-256("httpwire tag\x00" ||
// epoch), so a tag never carries the raw epoch of the open it names.
func OpenToken(epoch [16]byte) [8]byte {
	h := sha256.New()
	h.Write([]byte("httpwire tag\x00"))
	h.Write(epoch[:])
	var t [8]byte
	copy(t[:], h.Sum(nil))
	return t
}

// String answers "<16 lowercase hex>.<canonical decimal>": the value inside
// If-Match's quotes, and a read answer's tag field.
func (t Tag) String() string {
	return hex.EncodeToString(t.Open[:]) + "." + strconv.FormatUint(t.Rev, 10)
}

// MarshalText answers String's text.
func (t Tag) MarshalText() ([]byte, error) { return []byte(t.String()), nil }

var errTag = errors.New("httpwire: a tag is 16 lowercase hex characters, a dot and a canonical decimal")

// UnmarshalText takes exactly String's form: 16 lowercase hex characters,
// ".", and a decimal with no sign or leading zero of at most 2^64-1. On an
// error t is left unchanged.
func (t *Tag) UnmarshalText(b []byte) error {
	open, rev, ok := strings.Cut(string(b), ".")
	if !ok || len(open) != 2*len(t.Open) || strings.ToLower(open) != open || len(rev) > 1 && rev[0] == '0' {
		return errTag
	}
	// ParseUint in base 10 refuses a sign, an underscore and an empty text.
	n, err := strconv.ParseUint(rev, 10, 64)
	if err != nil {
		return errTag
	}
	var o [8]byte
	if _, err := hex.Decode(o[:], []byte(open)); err != nil {
		return errTag
	}
	t.Open, t.Rev = o, n
	return nil
}

const (
	headerIfMatch     = "If-Match"
	headerIfNoneMatch = "If-None-Match"
)

// ifMatch reads an IfMatch row's If-Match: one strong entity tag holding a
// Tag (RFC 9110 section 13.1.1). A missing header is if_match_required
// (RFC 6585 section 3); "*", a list, a weak tag or any other value is
// if_match_invalid.
func ifMatch(r *http.Request) (Tag, *Error) {
	vs := r.Header.Values(headerIfMatch)
	if len(vs) == 0 {
		return Tag{}, &Error{Code: codeIfMatchReq, Message: "this write needs If-Match naming the tag a read returned"}
	}
	var t Tag
	if len(vs) > 1 || len(vs[0]) < 2 || vs[0][0] != '"' || vs[0][len(vs[0])-1] != '"' ||
		t.UnmarshalText([]byte(vs[0][1:len(vs[0])-1])) != nil {
		return Tag{}, &Error{Code: codeIfMatchBad, Message: "If-Match is not one quoted tag a read returned"}
	}
	return t, nil
}
