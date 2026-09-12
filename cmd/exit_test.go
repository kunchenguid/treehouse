package cmd

import (
	"errors"
	"fmt"
	"testing"
)

// A generic failure must keep the plain failure status: only errors that were
// deliberately tagged carry a distinct code.
func TestExitCodeDefaultsToFailure(t *testing.T) {
	if code := ExitCode(errors.New("boom")); code != ExitFailure {
		t.Fatalf("expected untagged error to exit %d, got %d", ExitFailure, code)
	}
}

func TestExitCodeReportsSuccessForNil(t *testing.T) {
	if code := ExitCode(nil); code != 0 {
		t.Fatalf("expected nil error to exit 0, got %d", code)
	}
}

// The tag must survive wrapping: RunE results travel through fmt.Errorf on
// their way out of cobra, and a lost tag silently degrades to exit 1.
func TestExitCodeSurvivesWrapping(t *testing.T) {
	tagged := withExitCode(ExitNotReturned, errors.New("worktree not returned"))
	wrapped := fmt.Errorf("context: %w", tagged)

	if code := ExitCode(wrapped); code != ExitNotReturned {
		t.Fatalf("expected wrapped tagged error to exit %d, got %d", ExitNotReturned, code)
	}
	if !errors.Is(wrapped, tagged) {
		t.Fatal("expected the tagged error to remain matchable through the wrap")
	}
}

// withExitCode is applied unconditionally at call sites, so a nil error must
// not become a non-nil failure carrying an exit status.
func TestWithExitCodeLeavesNilAlone(t *testing.T) {
	if err := withExitCode(ExitNotReturned, nil); err != nil {
		t.Fatalf("expected nil to stay nil, got %v", err)
	}
}

// The tagged error must read as its cause: Execute prints it verbatim.
func TestExitCodeErrorReportsUnderlyingMessage(t *testing.T) {
	cause := errors.New("worktree not returned: it has uncommitted changes")
	tagged := withExitCode(ExitNotReturned, cause)

	if tagged.Error() != cause.Error() {
		t.Fatalf("expected %q, got %q", cause.Error(), tagged.Error())
	}
	if !errors.Is(tagged, cause) {
		t.Fatal("expected the cause to be unwrappable")
	}
}
