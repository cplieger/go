package textsafe

import (
	"slices"
	"strings"
	"unicode/utf8"
)

const redactedMark = "[redacted]"

// RedactSingleLineCapped sanitizes s like SanitizeSingleLine, writing
// "[redacted]" in place of every rune that an occurrence of a non-empty secret
// touches, in s or in its sanitized form, one mark per run of such runes; then
// caps like SanitizeSingleLineCapped, dropping a trailing proper prefix of a
// secret after a cut. No occurrence in s or its sanitized form survives, though
// a mark and its neighbours can spell a secret that shares text with
// "[redacted]". Pass s whole: a secret your own truncation split survives as
// its prefix whenever s then fits n.
func RedactSingleLineCapped(s string, n int, marker string, secrets ...string) (text string, cut bool) {
	needles := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			needles = append(needles, secret)
		}
	}
	slices.Sort(needles)
	needles = slices.Compact(needles)

	t := redact(s, needles)
	if t == "" || len(t) <= n {
		return t, false
	}
	head, mark := markedCut(t, n, marker)
	return trimSecretPrefix(head, needles) + mark, true
}

// redact is SanitizeSingleLine(s) with one redactedMark per run of runes that
// an occurrence of a needle touches, in s or in that sanitized form. Every
// occurrence is found before any rune is replaced, so no mark can break a match.
func redact(s string, needles []string) string {
	t := SanitizeSingleLine(s)
	inS, inT := occurrences(s, needles), occurrences(t, needles)
	if inS == nil && inT == nil {
		return t
	}
	var b strings.Builder
	marking := false
	// SanitizeSingleLine maps each rune of s, a malformed byte included, to
	// exactly one rune of t, so i and j step through the same rune; a
	// sanitizer that dropped or split a rune would misalign the two flag sets.
	for i, j := 0, 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:])
		_, width := utf8.DecodeRuneInString(t[j:])
		switch {
		case !touched(inS, i, i+size) && !touched(inT, j, j+width):
			b.WriteString(t[j : j+width])
			marking = false
		case !marking:
			b.WriteString(redactedMark)
			marking = true
		}
		i, j = i+size, j+width
	}
	return b.String()
}

// occurrences flags every byte of text that an occurrence of a needle covers,
// overlapping occurrences included, or returns nil when no needle occurs.
func occurrences(text string, needles []string) []bool {
	var hit []bool
	for _, needle := range needles {
		marked := 0
		for from := 0; ; {
			i := strings.Index(text[from:], needle)
			if i < 0 {
				break
			}
			start := from + i
			if hit == nil {
				hit = make([]bool, len(text))
			}
			// Starting past the bytes already flagged writes each byte once,
			// however often a self-overlapping needle ("aaa" in a run of "a")
			// matches.
			for k := max(start, marked); k < start+len(needle); k++ {
				hit[k] = true
			}
			marked, from = start+len(needle), start+1
		}
	}
	return hit
}

func touched(hit []bool, from, to int) bool {
	return hit != nil && slices.Contains(hit[from:to], true)
}

// trimSecretPrefix drops the longest tail of h that is a proper prefix of a
// needle, then backs off to the start of the rune the cut lands in, since a
// needle that is not valid UTF-8 can match from inside one of h's runes.
func trimSecretPrefix(h string, needles []string) string {
	cut := len(h)
	for _, needle := range needles {
		for k := min(len(needle)-1, len(h)); k > len(h)-cut; k-- {
			if strings.HasSuffix(h, needle[:k]) {
				cut = len(h) - k
				break
			}
		}
	}
	for cut > 0 && cut < len(h) && !utf8.RuneStart(h[cut]) {
		cut--
	}
	return h[:cut]
}
