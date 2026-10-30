//go:build windows

package ui

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// newCygwinPtyPipe creates a named pipe whose name follows the MSYS2/Cygwin pty
// pattern go-isatty recognizes, the way Git Bash and mintty hand a terminal to a
// native Windows process. It returns the server end, to be used as stdin, and
// the client end to write answers to.
func newCygwinPtyPipe(t *testing.T) (server, client *os.File) {
	t.Helper()
	name := fmt.Sprintf(`\\.\pipe\msys-%016x-pty0-to-master`, time.Now().UnixNano())
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := windows.CreateNamedPipe(namePtr, windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT, 1, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatalf("CreateNamedPipe(%s): %v", name, err)
	}
	ch, err := windows.CreateFile(namePtr, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		windows.CloseHandle(sh)
		t.Fatalf("CreateFile(%s): %v", name, err)
	}
	return os.NewFile(uintptr(sh), name), os.NewFile(uintptr(ch), name)
}

// TestRealDetectorTreatsCygwinPtyPipeAsInteractive runs the real go-isatty
// detectors, with no injected stand-ins: an interactive Git Bash/mintty stdin is
// a named pipe to Go, and it must be classified as a terminal so the operator is
// allowed to take longer than the bounded wait to answer.
func TestRealDetectorTreatsCygwinPtyPipeAsInteractive(t *testing.T) {
	server, client := newCygwinPtyPipe(t)
	defer server.Close()
	defer client.Close()
	withStdin(t, server)

	if !stdinIsTerminal() {
		t.Fatal("a Cygwin/MSYS2 pty pipe on stdin must be classified as interactive")
	}

	oldWait := stdinAnswerWait
	stdinAnswerWait = 50 * time.Millisecond
	t.Cleanup(func() { stdinAnswerWait = oldWait })

	go func() {
		time.Sleep(500 * time.Millisecond)
		client.WriteString("n\n")
	}()
	ok, err := Confirm("ok?", true)
	if err != nil || ok {
		t.Fatalf("expected the delayed answer to be honored without a timeout, got ok=%v err=%v", ok, err)
	}
}

// TestRealDetectorKeepsOrdinaryPipeOnTheTimedPath is the counterpart: an
// anonymous pipe is not a terminal, so an unanswered prompt still fails closed.
func TestRealDetectorKeepsOrdinaryPipeOnTheTimedPath(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	withStdin(t, r)

	if stdinIsTerminal() {
		t.Fatal("an anonymous pipe on stdin must not be classified as interactive")
	}

	oldWait := stdinAnswerWait
	stdinAnswerWait = 50 * time.Millisecond
	t.Cleanup(func() { stdinAnswerWait = oldWait })

	if _, err := Confirm("ok?", true); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("expected ErrNoAnswer on an unanswered ordinary pipe, got %v", err)
	}
}
