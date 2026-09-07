package config

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound      = errors.New("config resource not found")
	ErrAlreadyExists = errors.New("config resource already exists")
	ErrInvalidInput  = errors.New("invalid config input")
)

type mutationError struct {
	kind  error
	cause error
}

func (e *mutationError) Error() string        { return e.cause.Error() }
func (e *mutationError) Unwrap() error        { return e.cause }
func (e *mutationError) Is(target error) bool { return target == e.kind }

// Errorf classifies a mutation error without altering its human-readable text.
// Formatting supports %w, preserving underlying validation errors as causes.
// Only known input/resource errors should be classified; storage errors remain
// unclassified so callers can distinguish unexpected transaction failures.
func Errorf(kind error, format string, args ...any) error {
	return &mutationError{kind: kind, cause: fmt.Errorf(format, args...)}
}
