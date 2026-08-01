package domain

import (
	"context"
	"errors"
	"testing"
)

func TestTypedErrorClassification(t *testing.T) {
	cause := errors.New("disk failure")
	err := ResourceError(ErrorCodeDependency, "git.clone", "repo", "clone failed", cause)

	if !errors.Is(err, cause) {
		t.Fatal("typed error does not unwrap its cause")
	}
	if !errors.Is(err, &Error{Code: ErrorCodeDependency}) {
		t.Fatal("typed error cannot be matched by code")
	}
	if !IsCode(err, ErrorCodeDependency) || CodeOf(err) != ErrorCodeDependency {
		t.Fatalf("unexpected classification: %q", CodeOf(err))
	}
	if got := err.Error(); got != "git.clone: dependency_unavailable: clone failed" {
		t.Fatalf("unexpected error text: %q", got)
	}
}

func TestCodeOfContextErrors(t *testing.T) {
	if got := CodeOf(context.Canceled); got != ErrorCodeCanceled {
		t.Fatalf("CodeOf(context.Canceled) = %q", got)
	}
	if got := CodeOf(context.DeadlineExceeded); got != ErrorCodeTimeout {
		t.Fatalf("CodeOf(context.DeadlineExceeded) = %q", got)
	}
}
