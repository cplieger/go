package feedwire

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	errPositionEmpty  = errors.New("feedwire: Feed-Position needs at least one member")
	errPositionSyntax = errors.New("feedwire: Feed-Position is not an RFC 9651 dictionary")
	errPositionSource = errors.New("feedwire: Feed-Position key is not a source name")
	errPositionRev    = errors.New("feedwire: Feed-Position member is not an Integer from 0 to MaxRev")
	errPositionEpoch  = errors.New("feedwire: Feed-Position member has no epoch parameter of 32 lowercase hex")
	errPositionRepeat = errors.New("feedwire: Feed-Position names a source twice")
)

// FormatPosition writes the Feed-Position value for ps, an RFC 9651
// dictionary with one member per source in the given order, each
// `<source>=<rev>;epoch="<epoch>"`, joined by ", ". It refuses an empty list,
// a repeated source and any value outside the bounds on Position.
func FormatPosition(ps []Position) (string, error) {
	if len(ps) == 0 {
		return "", errPositionEmpty
	}
	var b strings.Builder
	for i, p := range ps {
		switch {
		case !ValidSource(p.Source):
			return "", errPositionSource
		case !ValidEpoch(p.Epoch):
			return "", errPositionEpoch
		case p.Rev > MaxRev:
			return "", errPositionRev
		case slices.ContainsFunc(ps[:i], func(q Position) bool { return q.Source == p.Source }):
			return "", errPositionRepeat
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.Source)
		b.WriteByte('=')
		b.WriteString(strconv.FormatUint(p.Rev, 10))
		b.WriteString(`;epoch="`)
		b.WriteString(p.Epoch)
		b.WriteByte('"')
	}
	return b.String(), nil
}

// ParsePosition parses a Feed-Position value as an RFC 9651 dictionary (a
// repeated key overwrites its value in place) and returns its members in
// order. Each member must be an Integer from 0 to MaxRev under a ValidSource
// key, with an epoch parameter that is a String of 32 lowercase hex; other
// parameters are ignored, and any other member refuses the whole value. An
// empty value is an empty dictionary: no position and no error.
func ParsePosition(v string) ([]Position, error) {
	members, err := parseDictionary(v)
	if err != nil {
		return nil, err
	}
	var ps []Position
	for _, m := range members {
		switch {
		case !ValidSource(m.key):
			return nil, errPositionSource
		case m.inner || m.item.kind != sfInteger || m.item.num < 0 || m.item.num > MaxRev:
			return nil, errPositionRev
		case m.epoch.kind != sfString || !ValidEpoch(m.epoch.str):
			return nil, errPositionEpoch
		}
		ps = append(ps, Position{Source: m.key, Epoch: m.epoch.str, Rev: uint64(m.item.num)})
	}
	return ps, nil
}

// epochParam is the Feed-Position member parameter holding the source's epoch.
const epochParam = "epoch"

type sfKind byte

const (
	sfNone sfKind = iota
	sfInteger
	sfString
	sfOther
)

type sfItem struct {
	str  string
	num  int64
	kind sfKind
}

// sfMember keeps what ParsePosition reads of a dictionary member: its bare
// item, whether it was an Inner List, and its last epoch parameter.
type sfMember struct {
	key   string
	item  sfItem
	epoch sfItem
	inner bool
}

// sfParser follows the parsing algorithms of RFC 9651 section 4.2.
type sfParser struct {
	s string
	i int
}

func (p *sfParser) peek() byte {
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

func (p *sfParser) done() bool { return p.i >= len(p.s) }

func (p *sfParser) skipSP() {
	for p.peek() == ' ' {
		p.i++
	}
}

func (p *sfParser) skipOWS() {
	for c := p.peek(); c == ' ' || c == '\t'; c = p.peek() {
		p.i++
	}
}

// parseDictionary is RFC 9651 sections 4.2 and 4.2.2: the input must be
// ASCII, leading and trailing SP are discarded, and a repeated key keeps its
// first place with its last value.
func parseDictionary(v string) ([]sfMember, error) {
	for i := range len(v) {
		if v[i] >= utf8.RuneSelf {
			return nil, errPositionSyntax
		}
	}
	p := &sfParser{s: v}
	p.skipSP()
	var members []sfMember
	for !p.done() {
		m, err := p.member()
		if err != nil {
			return nil, err
		}
		if j := slices.IndexFunc(members, func(o sfMember) bool { return o.key == m.key }); j >= 0 {
			members[j] = m
		} else {
			members = append(members, m)
		}
		p.skipOWS()
		if p.done() {
			break
		}
		if p.peek() != ',' {
			return nil, errPositionSyntax
		}
		p.i++
		p.skipOWS()
		if p.done() {
			return nil, errPositionSyntax
		}
	}
	return members, nil
}

func (p *sfParser) member() (sfMember, error) {
	key, err := p.key()
	if err != nil {
		return sfMember{}, err
	}
	m := sfMember{key: key, item: sfItem{kind: sfOther}}
	if p.peek() == '=' {
		p.i++
		if p.peek() == '(' {
			m.inner = true
			err = p.innerList()
		} else {
			m.item, err = p.bareItem()
		}
		if err != nil {
			return sfMember{}, err
		}
	}
	m.epoch, err = p.parameters()
	return m, err
}

// key is RFC 9651 section 4.2.3.3.
func (p *sfParser) key() (string, error) {
	start := p.i
	if c := p.peek(); (c < 'a' || c > 'z') && c != '*' {
		return "", errPositionSyntax
	}
	for {
		c := p.peek()
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' && c != '.' && c != '*' {
			return p.s[start:p.i], nil
		}
		p.i++
	}
}

// parameters is RFC 9651 section 4.2.3.2; it returns the last epoch value.
func (p *sfParser) parameters() (sfItem, error) {
	var epoch sfItem
	for p.peek() == ';' {
		p.i++
		p.skipSP()
		key, err := p.key()
		if err != nil {
			return sfItem{}, err
		}
		value := sfItem{kind: sfOther}
		if p.peek() == '=' {
			p.i++
			if value, err = p.bareItem(); err != nil {
				return sfItem{}, err
			}
		}
		if key == epochParam {
			epoch = value
		}
	}
	return epoch, nil
}

// innerList is RFC 9651 section 4.2.1.2, read for its syntax only.
func (p *sfParser) innerList() error {
	p.i++
	for !p.done() {
		p.skipSP()
		if p.peek() == ')' {
			p.i++
			return nil
		}
		if _, err := p.bareItem(); err != nil {
			return err
		}
		if _, err := p.parameters(); err != nil {
			return err
		}
		if c := p.peek(); c != ' ' && c != ')' {
			return errPositionSyntax
		}
	}
	return errPositionSyntax
}

// bareItem is RFC 9651 section 4.2.3.1. Only Integers and Strings keep their
// value; every other type is checked for its syntax.
func (p *sfParser) bareItem() (sfItem, error) {
	switch c := p.peek(); {
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	case c == '"':
		return p.str()
	}
	return sfItem{kind: sfOther}, p.otherItem()
}

func (p *sfParser) otherItem() error {
	switch c := p.peek(); {
	case c == '*' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		p.token()
		return nil
	case c == ':':
		return p.byteSequence()
	case c == '?':
		return p.boolean()
	case c == '@':
		p.i++
		if it, err := p.number(); err != nil || it.kind != sfInteger {
			return errPositionSyntax
		}
		return nil
	case c == '%':
		return p.displayString()
	}
	return errPositionSyntax
}

// boolean is RFC 9651 section 4.2.8.
func (p *sfParser) boolean() error {
	p.i++
	if c := p.peek(); c != '0' && c != '1' {
		return errPositionSyntax
	}
	p.i++
	return nil
}

// number is RFC 9651 section 4.2.4: an Integer has at most 15 digits; a
// Decimal at most 12 before its point and 1 to 3 after it.
func (p *sfParser) number() (sfItem, error) {
	start := p.i
	if p.peek() == '-' {
		p.i++
	}
	if c := p.peek(); c < '0' || c > '9' {
		return sfItem{}, errPositionSyntax
	}
	digits, dot := 0, -1
	for {
		c := p.peek()
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == '.' && dot < 0:
			if digits > 12 {
				return sfItem{}, errPositionSyntax
			}
			dot = digits
		default:
			return p.endNumber(start, digits, dot)
		}
		p.i++
		if digits > 15 {
			return sfItem{}, errPositionSyntax
		}
	}
}

func (p *sfParser) endNumber(start, digits, dot int) (sfItem, error) {
	if dot >= 0 {
		if frac := digits - dot; frac < 1 || frac > 3 {
			return sfItem{}, errPositionSyntax
		}
		return sfItem{kind: sfOther}, nil
	}
	n, err := strconv.ParseInt(p.s[start:p.i], 10, 64)
	if err != nil {
		return sfItem{}, errPositionSyntax
	}
	return sfItem{kind: sfInteger, num: n}, nil
}

// str is RFC 9651 section 4.2.5.
func (p *sfParser) str() (sfItem, error) {
	p.i++
	var b strings.Builder
	for !p.done() {
		c := p.s[p.i]
		p.i++
		switch {
		case c == '\\':
			if e := p.peek(); e != '"' && e != '\\' {
				return sfItem{}, errPositionSyntax
			}
			b.WriteByte(p.s[p.i])
			p.i++
		case c == '"':
			return sfItem{kind: sfString, str: b.String()}, nil
		case c < 0x20 || c > 0x7e:
			return sfItem{}, errPositionSyntax
		default:
			b.WriteByte(c)
		}
	}
	return sfItem{}, errPositionSyntax
}

// token is RFC 9651 section 4.2.6: tchar, ":" and "/" after the first.
func (p *sfParser) token() {
	p.i++
	for !p.done() {
		c := p.s[p.i]
		if c != ':' && c != '/' && !isTchar(c) {
			return
		}
		p.i++
	}
}

func isTchar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// byteSequence is RFC 9651 section 4.2.7, checked for its alphabet only:
// the value is never decoded.
func (p *sfParser) byteSequence() error {
	p.i++
	for !p.done() {
		c := p.s[p.i]
		p.i++
		switch {
		case c == ':':
			return nil
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/', c == '=':
		default:
			return errPositionSyntax
		}
	}
	return errPositionSyntax
}

// displayString is RFC 9651 section 4.2.10.
func (p *sfParser) displayString() error {
	p.i++
	if p.peek() != '"' {
		return errPositionSyntax
	}
	p.i++
	var b []byte
	for !p.done() {
		c := p.s[p.i]
		p.i++
		switch {
		case c < 0x20 || c > 0x7e:
			return errPositionSyntax
		case c == '%':
			if p.i+2 > len(p.s) || !lowerHex(p.s[p.i:p.i+2], 2) {
				return errPositionSyntax
			}
			n, _ := strconv.ParseUint(p.s[p.i:p.i+2], 16, 8)
			b = append(b, byte(n))
			p.i += 2
		case c == '"':
			if !utf8.Valid(b) {
				return errPositionSyntax
			}
			return nil
		default:
			b = append(b, c)
		}
	}
	return errPositionSyntax
}
