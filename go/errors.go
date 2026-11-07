package trak

import (
	"errors"
	"fmt"
)

// FormatError reports bytes that don't follow the format. Offset is the byte
// where the problem was found, or -1 when unknown.
type FormatError struct {
	Msg    string
	Offset int64
}

func (e *FormatError) Error() string {
	if e.Offset < 0 {
		return e.Msg
	}
	return fmt.Sprintf("%s (at byte %d)", e.Msg, e.Offset)
}

func formatErr(offset int64, format string, args ...any) *FormatError {
	return &FormatError{Msg: fmt.Sprintf(format, args...), Offset: offset}
}

// UnsupportedVersionError reports a file whose major version is not 1. It
// unwraps to a *FormatError.
type UnsupportedVersionError struct {
	Version string
}

func (e *UnsupportedVersionError) Error() string {
	return fmt.Sprintf("unsupported major version %s (at byte 5)", e.Version)
}

func (e *UnsupportedVersionError) Unwrap() error {
	return &FormatError{Msg: "unsupported major version " + e.Version, Offset: 5}
}

// SchemaError reports an invalid schema definition. Line and Column are
// 1-based, or 0 when unknown.
type SchemaError struct {
	Msg          string
	Line, Column int
}

func (e *SchemaError) Error() string {
	if e.Line == 0 {
		return e.Msg
	}
	return fmt.Sprintf("%s (line %d, column %d)", e.Msg, e.Line, e.Column)
}

// ValueError reports a value that doesn't fit its schema: a bad num or enum,
// a missing field, or similar.
type ValueError struct {
	Msg string
}

func (e *ValueError) Error() string { return e.Msg }

func valueErr(format string, args ...any) *ValueError {
	return &ValueError{Msg: fmt.Sprintf(format, args...)}
}

var (
	// ErrLocked is returned when another writer holds the file's lock.
	ErrLocked = errors.New("trak: file is locked by another writer")
	// ErrClosed is returned when using a closed reader or writer.
	ErrClosed = errors.New("trak: closed")
	// ErrNotFound is returned for an unknown type or event number.
	ErrNotFound = errors.New("trak: not found")
)
