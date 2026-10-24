package domain

import (
	"context"
	"errors"
)

// ErrorCode is stable across CLI, TUI, and HTTP error presentation.
type ErrorCode string

const (
	ErrorCodeInvalidArgument ErrorCode = "invalid_argument"
	ErrorCodeNotFound        ErrorCode = "not_found"
	ErrorCodeAlreadyExists   ErrorCode = "already_exists"
	ErrorCodeConflict        ErrorCode = "conflict"
	ErrorCodeUnauthorized    ErrorCode = "unauthorized"
	ErrorCodeForbidden       ErrorCode = "forbidden"
	ErrorCodeDependency      ErrorCode = "dependency_unavailable"
	ErrorCodeConfig          ErrorCode = "config"
	ErrorCodeCanceled        ErrorCode = "canceled"
	ErrorCodeTimeout         ErrorCode = "timeout"
	ErrorCodeInternal        ErrorCode = "internal"
)

// Error carries machine-readable classification without exposing transport details.
type Error struct {
	Code     ErrorCode `json:"code"`
	Op       string    `json:"operation,omitempty"`
	Field    string    `json:"field,omitempty"`
	Resource string    `json:"resource,omitempty"`
	Message  string    `json:"message"`
	Err      error     `json:"-"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	prefix := string(e.Code)
	if e.Op != "" {
		prefix = e.Op + ": " + prefix
	}
	if e.Field != "" {
		prefix += " (" + e.Field + ")"
	}
	if e.Message != "" {
		return prefix + ": " + e.Message
	}
	if e.Err != nil {
		return prefix + ": " + e.Err.Error()
	}
	return prefix
}

func (e *Error) Unwrap() error { return e.Err }

// Is permits errors.Is matching by any populated fields on the target Error.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	if !ok {
		return false
	}
	return (other.Code == "" || e.Code == other.Code) &&
		(other.Op == "" || e.Op == other.Op) &&
		(other.Field == "" || e.Field == other.Field) &&
		(other.Resource == "" || e.Resource == other.Resource)
}

func NewError(code ErrorCode, op, message string, err error) *Error {
	return &Error{Code: code, Op: op, Message: message, Err: err}
}

func FieldError(code ErrorCode, op, field, message string) *Error {
	return &Error{Code: code, Op: op, Field: field, Message: message}
}

func ResourceError(code ErrorCode, op, resource, message string, err error) *Error {
	return &Error{Code: code, Op: op, Resource: resource, Message: message, Err: err}
}

func IsCode(err error, code ErrorCode) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Code == code
}

func CodeOf(err error) ErrorCode {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return ErrorCodeCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorCodeTimeout
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ErrorCodeInternal
}
