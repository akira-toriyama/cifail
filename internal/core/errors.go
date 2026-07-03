// Package core holds cifail's cross-cutting types: the process exit-code
// contract and the structured error the CLI renders to stderr. It has no
// dependencies on the other internal packages so everything can import it.
package core

import (
	"errors"
	"fmt"
)

// Code is cifail's process exit-code contract. The CLI maps a returned error to
// one of these on the way out. Keep the meanings stable — scripts and Claude
// Code branch on them.
type Code int

const (
	CodeOK        Code = 0 // a failure run was found and its logs were extracted
	CodeNoFailure Code = 1 // no failing run found for the target (a soft miss, not an error)
	CodeUsage     Code = 2 // bad usage or invalid input — fix the args, do not retry
	CodeAPI       Code = 3 // GitHub API / network / IO failure
)

// Error is cifail's structured error. On a non-zero exit the CLI prints it to
// stderr as {"error":{"code","message"[,"details"]}} so callers get a
// machine-readable failure. Plain (non-*Error) errors are treated as CodeAPI.
type Error struct {
	Code Code
	Msg  string
	// Details is optional machine-actionable payload for errors where the
	// message alone isn't enough to act on. Rendered as "details" in the
	// envelope when non-nil.
	Details any
}

func (e *Error) Error() string { return e.Msg }

// Usagef builds a CodeUsage error (bad input — do not retry).
func Usagef(format string, a ...any) *Error {
	return &Error{Code: CodeUsage, Msg: fmt.Sprintf(format, a...)}
}

// APIf builds a CodeAPI error (GitHub API / network / IO failure).
func APIf(format string, a ...any) *Error {
	return &Error{Code: CodeAPI, Msg: fmt.Sprintf(format, a...)}
}

// NoFailuref builds a CodeNoFailure error (nothing matched — a soft miss).
func NoFailuref(format string, a ...any) *Error {
	return &Error{Code: CodeNoFailure, Msg: fmt.Sprintf(format, a...)}
}

// ExitCode resolves any error to a process exit code. nil -> 0; a *core.Error
// -> its Code; anything else -> CodeAPI (an unclassified failure is treated as
// an IO/API failure by definition).
func ExitCode(err error) int {
	if err == nil {
		return int(CodeOK)
	}
	var ce *Error
	if errors.As(err, &ce) {
		return int(ce.Code)
	}
	return int(CodeAPI)
}

// AsError returns the *Error in err's chain, or nil, when the CLI needs the
// structured fields rather than just the exit code.
func AsError(err error) *Error {
	var ce *Error
	if errors.As(err, &ce) {
		return ce
	}
	return nil
}
