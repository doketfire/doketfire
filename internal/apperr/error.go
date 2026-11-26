// Package apperr defines application errors that cross the HTTP boundary.
// Internal errors are wrapped with %w and never exposed to clients.
package apperr

import "errors"

// Code is a stable error code returned by the API.
type Code string

const (
	CodeValidation   Code = "validation_error"
	CodeUnauthorized Code = "unauthorized"
	CodeForbidden    Code = "forbidden"
	CodeNotFound     Code = "not_found"
	CodeConflict     Code = "conflict"
	CodeRateLimited  Code = "rate_limited"
	CodePayloadLarge Code = "payload_too_large"
	CodeQueryTimeout Code = "query_timeout"
	CodeInternal     Code = "internal_error"
)

// Error carries a stable code, a safe client message and an internal cause.
type Error struct {
	Code    Code
	Message string
	Details map[string]any
	Op      string
	Err     error
}

// Error implements error.
func (e *Error) Error() string { return e.Op + ": " + e.Message }

// Unwrap exposes the internal cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.Err }

// E builds an application error. The message must be safe to return to clients.
func E(op string, code Code, msg string, err error) *Error {
	return &Error{Op: op, Code: code, Message: msg, Err: err}
}

// CodeOf returns the code of the first *Error in the chain, or CodeInternal.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// WithDetail adds a validation detail and returns the same error for chaining.
func (e *Error) WithDetail(key string, value any) *Error {
	if e.Details == nil {
		e.Details = make(map[string]any)
	}
	e.Details[key] = value
	return e
}

// IsCode reports whether err carries the given application code.
func IsCode(err error, code Code) bool { return CodeOf(err) == code }
