package sse

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Wire is the revision of the stream contract every hello declares.
const Wire = 1

// MaxFrameBytes is the largest encoded frame, terminating blank line
// included. Publish and Writer.Event return ErrFrameTooLarge above it.
const MaxFrameBytes = 1 << 20

const (
	reservedPrefix        = "sse:"
	helloEvent            = "sse:hello"
	resetEvent            = "sse:reset"
	defaultKeepaliveEvent = "sse:keepalive"
)

var (
	// ErrFrameTooLarge is returned by Publish and Writer.Event when the encoded
	// frame would exceed MaxFrameBytes; it is wrapped with the size and the cap.
	ErrFrameTooLarge = errors.New("sse: frame exceeds MaxFrameBytes")
	// ErrInvalidUTF8 is returned by Publish and Writer.Event when Data is not
	// valid UTF-8; it is wrapped with the byte offset of the first invalid byte.
	ErrInvalidUTF8 = errors.New("sse: data is not valid UTF-8")
	// ErrEventName is returned by Publish and Writer.Event for a name the
	// encoder or the client would misread: one with the reserved "sse:"
	// prefix, a CR or LF, or the hub's configured keepalive name.
	ErrEventName = errors.New("sse: event name not allowed")
)

// checkEventName refuses a name the encoder or the client would misread: the
// reserved "sse:" prefix, a CR or LF, or the hub's configured keepalive name.
// It is an error, not a panic, because a name can come from configuration.
// The error never quotes the name, which may carry the CR or LF it refuses.
func checkEventName(name, keepalive string) error {
	if strings.HasPrefix(name, reservedPrefix) {
		return fmt.Errorf("%w: the %q prefix is reserved", ErrEventName, reservedPrefix)
	}
	if strings.ContainsAny(name, "\r\n") {
		return fmt.Errorf("%w: a name cannot hold CR or LF", ErrEventName)
	}
	if name != "" && name == keepalive {
		return fmt.Errorf("%w: it is the configured keepalive event", ErrEventName)
	}
	return nil
}

func checkData(data []byte) error {
	if utf8.Valid(data) {
		return nil
	}
	return fmt.Errorf("%w: invalid byte at offset %d", ErrInvalidUTF8, invalidUTF8Offset(data))
}

func invalidUTF8Offset(data []byte) int {
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size <= 1 {
			return i
		}
		i += size
	}
	return len(data)
}

// splitDataLines splits data on CRLF, CR and LF alike, one element per
// data: line. Empty data yields one empty line.
func splitDataLines(data []byte) [][]byte {
	var lines [][]byte
	if bytes.IndexByte(data, '\r') < 0 {
		for {
			i := bytes.IndexByte(data, '\n')
			if i < 0 {
				return append(lines, data)
			}
			lines = append(lines, data[:i])
			data = data[i+1:]
		}
	}
	for {
		i := bytes.IndexAny(data, "\r\n")
		if i < 0 {
			return append(lines, data)
		}
		lines = append(lines, data[:i])
		if data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			i++
		}
		data = data[i+1:]
	}
}

// frameSize is the one definition of a frame's byte count: the id: line when
// idLen > 0, the event: line when nameLen > 0, every data: line, each with its
// LF, plus the terminating blank line.
func frameSize(nameLen int, lines [][]byte, idLen int) int {
	n := 1
	if idLen > 0 {
		n += len("id: ") + idLen + 1
	}
	if nameLen > 0 {
		n += len("event: ") + nameLen + 1
	}
	for _, line := range lines {
		n += len("data: ") + len(line) + 1
	}
	return n
}

// idLineBytes is the size of "id: <epoch>:<offset>\n" for an offset of the
// given digit count, the one term of a frame's size that depends on where in
// the sequence it lands.
func idLineBytes(offsetDigits int) int {
	return len("id: ") + epochLen + 1 + offsetDigits + 1
}

func digits(n uint64) int {
	d := 1
	for n >= 10 {
		n /= 10
		d++
	}
	return d
}

func tooLarge(size int) error {
	return fmt.Errorf("%w: %d bytes, cap %d", ErrFrameTooLarge, size, MaxFrameBytes)
}

func writeFrame(w io.Writer, id, name string, lines [][]byte) error {
	var buf bytes.Buffer
	buf.Grow(frameSize(len(name), lines, len(id)))
	if id != "" {
		buf.WriteString("id: ")
		buf.WriteString(id)
		buf.WriteByte('\n')
	}
	if name != "" {
		buf.WriteString("event: ")
		buf.WriteString(name)
		buf.WriteByte('\n')
	}
	for _, line := range lines {
		buf.WriteString("data: ")
		buf.Write(line)
		buf.WriteByte('\n')
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}
