package apperr

import (
	"errors"
	"fmt"
	"testing"
)

func TestCodeOf(t *testing.T) {
	base := E("query.list", CodeValidation, "invalid filter", nil)
	if got := CodeOf(base); got != CodeValidation {
		t.Fatalf("CodeOf(base) = %q", got)
	}

	wrapped := fmt.Errorf("handler: %w", base)
	if got := CodeOf(wrapped); got != CodeValidation {
		t.Fatalf("CodeOf(wrapped) = %q", got)
	}
	if !IsCode(wrapped, CodeValidation) {
		t.Fatal("IsCode(wrapped) = false")
	}

	if got := CodeOf(errors.New("boom")); got != CodeInternal {
		t.Fatalf("CodeOf(unknown) = %q", got)
	}
	if got := CodeOf(nil); got != CodeInternal {
		t.Fatalf("CodeOf(nil) = %q", got)
	}
	if IsCode(errors.New("boom"), CodeValidation) {
		t.Fatal("unknown error must not match a validation code")
	}
}

func TestErrorUnwrap(t *testing.T) {
	cause := errors.New("cause")
	err := E("op", CodeConflict, "conflict", cause)

	if err.Error() != "op: conflict" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is must find the wrapped cause")
	}
}

func TestWithDetailDoesNotShareMaps(t *testing.T) {
	first := E("a", CodeValidation, "a", nil).WithDetail("field", "title")
	second := E("b", CodeValidation, "b", nil)
	if second.Details != nil {
		t.Fatalf("new error shares details: %v", second.Details)
	}

	second.WithDetail("field", "body")
	if first.Details["field"] != "title" {
		t.Fatalf("first details mutated: %v", first.Details)
	}
	if second.Details["field"] != "body" {
		t.Fatalf("second details = %v", second.Details)
	}
}
