package filesafe

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
)

// saturateAdd returns a + b clamped to math.MaxInt64 on overflow.
func saturateAdd(a, b int64) int64 {
	sum := a + b
	if sum < a {
		return math.MaxInt64
	}
	return sum
}

// ReadBounded opens path and reads at most maxBytes from it, returning
// ErrFileTooLarge if the file exceeds maxBytes, including if it grows past
// the limit during the read. ctx is checked before the open and before the
// read. Only regular files are read: a directory, named pipe, device node or
// socket is refused with ErrNotRegular, and the open is non-blocking so a
// FIFO with no writer cannot stall the caller. It follows symlinks, unlike
// the write primitives: a caller reading from a directory a less-trusted
// principal can write uses ReadBoundedInRoot. On Windows its open handle
// does not block a filesafe write of the file.
//
// maxBytes 0 admits only an empty file, and a negative maxBytes returns
// ErrInvalidArgument: a read bound is never "no cap", unlike WithMaxBytes.
func ReadBounded(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	cleanPath, err := validateAbsClean(path)
	if err != nil {
		return nil, err
	}
	if capErr := checkReadCap(maxBytes); capErr != nil {
		return nil, capErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("filesafe: %w", ctxErr)
	}
	f, err := openRead(cleanPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, statErr := statRegular(f, cleanPath); statErr != nil {
		return nil, statErr
	}
	return ReadBoundedFile(ctx, f, maxBytes)
}

// checkReadCap refuses a negative read bound before anything is opened.
func checkReadCap(maxBytes int64) error {
	if maxBytes < 0 {
		return fmt.Errorf("%w: maxBytes %d is negative", ErrInvalidArgument, maxBytes)
	}
	return nil
}

// ReadBoundedFile is ReadBounded for a file the caller opened itself, for
// example through an *os.Root to confine the path: it reads at most maxBytes
// from f and returns ErrFileTooLarge if the file exceeds maxBytes, including
// if it grows past the limit during the read. ctx is checked before the size
// stat and before the read. The caller owns f; ReadBoundedFile does not close
// it. A nil f returns ErrUnsafePath, and maxBytes follows ReadBounded's rule.
func ReadBoundedFile(ctx context.Context, f *os.File, maxBytes int64) ([]byte, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("filesafe: %w", ctxErr)
	}
	if f == nil {
		return nil, fmt.Errorf("%w: nil file", ErrUnsafePath)
	}
	if capErr := checkReadCap(maxBytes); capErr != nil {
		return nil, capErr
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() > maxBytes {
		return nil, fmt.Errorf("%w: %d bytes (max %d)", ErrFileTooLarge, fi.Size(), maxBytes)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("filesafe: %w", ctxErr)
	}
	data, err := io.ReadAll(io.LimitReader(f, saturateAdd(maxBytes, 1)))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: file grew past %d byte limit during read", ErrFileTooLarge, maxBytes)
	}
	return data, nil
}
