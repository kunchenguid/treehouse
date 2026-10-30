package ui

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// withStdin swaps os.Stdin for the read end of r and resets the shared reader
// state, restoring everything afterwards.
func withStdin(t *testing.T, r *os.File) {
	t.Helper()
	stdinOnce.Do(func() {})
	oldStdin, oldReader, oldExhausted := os.Stdin, stdinReader, stdinExhausted
	os.Stdin = r
	stdinReader = bufio.NewReader(r)
	stdinExhausted = false
	t.Cleanup(func() {
		os.Stdin, stdinReader, stdinExhausted = oldStdin, oldReader, oldExhausted
	})
}

func TestConfirmTerminalWaitsForDelayedAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		native bool
		cygwin bool
	}{
		{name: "native", native: true},
		{name: "cygwin", cygwin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			withStdin(t, r)

			oldNative, oldCygwin, oldWait := isTerminal, isCygwinTerminal, stdinAnswerWait
			t.Cleanup(func() {
				isTerminal, isCygwinTerminal, stdinAnswerWait = oldNative, oldCygwin, oldWait
			})
			isTerminal = func(fd uintptr) bool {
				if fd != r.Fd() {
					t.Errorf("native terminal check used fd %v, want %v", fd, r.Fd())
				}
				return tc.native
			}
			isCygwinTerminal = func(fd uintptr) bool {
				if fd != r.Fd() {
					t.Errorf("Cygwin terminal check used fd %v, want %v", fd, r.Fd())
				}
				return tc.cygwin
			}
			stdinAnswerWait = 10 * time.Millisecond

			type answer struct {
				ok  bool
				err error
			}
			done := make(chan answer, 1)
			go func() {
				ok, err := Confirm("ok?", false)
				done <- answer{ok, err}
			}()
			select {
			case result := <-done:
				t.Fatalf("terminal prompt returned before answer: %+v", result)
			case <-time.After(100 * time.Millisecond):
			}
			if _, err := w.WriteString("y\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				if result.err != nil || !result.ok {
					t.Fatalf("expected delayed terminal confirmation, got %+v", result)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("terminal prompt did not consume the answer")
			}
		})
	}
}

func TestConfirmNullDeviceIsNotTerminal(t *testing.T) {
	r, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	withStdin(t, r)

	if stdinIsTerminal() {
		t.Fatal("null device classified as an interactive terminal")
	}
	if _, err := Confirm("ok?", true); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF from null stdin, got %v", err)
	}
}

func TestConfirmOpenUnwrittenPipeFailsClosed(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	withStdin(t, r)

	old := stdinAnswerWait
	stdinAnswerWait = 50 * time.Millisecond
	defer func() { stdinAnswerWait = old }()

	done := make(chan error, 1)
	go func() {
		_, err := Confirm("ok?", true)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNoAnswer) {
			t.Fatalf("expected ErrNoAnswer, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Confirm blocked on an open pipe nobody writes to")
	}
}

func TestConfirmPipedAnswerStillWorks(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	withStdin(t, r)

	if _, err := io.Copy(w, strings.NewReader("n\ny\nyes")); err != nil {
		t.Fatal(err)
	}
	w.Close()

	ok, err := Confirm("ok?", true)
	if err != nil || ok {
		t.Fatalf("expected an explicit no, got ok=%v err=%v", ok, err)
	}
	for i := 0; i < 2; i++ {
		ok, err := Confirm("ok?", false)
		if err != nil || !ok {
			t.Fatalf("expected an explicit yes, got ok=%v err=%v", ok, err)
		}
	}
}

func TestConfirmAnswerArrivingAfterTimeoutIsDiscarded(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	withStdin(t, r)

	old := stdinAnswerWait
	stdinAnswerWait = 20 * time.Millisecond
	defer func() { stdinAnswerWait = old }()

	if _, err := Confirm("ok?", true); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("expected ErrNoAnswer, got %v", err)
	}
	if _, err := w.WriteString("y\ny\n"); err != nil {
		t.Fatal(err)
	}
	stdinAnswerWait = 5 * time.Second
	for _, defaultYes := range []bool{true, false} {
		start := time.Now()
		_, err := Confirm("ok?", defaultYes)
		if !errors.Is(err, ErrNoAnswer) {
			t.Fatalf("expected late answers discarded, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("exhausted stdin waited %v instead of failing immediately", elapsed)
		}
	}
}
