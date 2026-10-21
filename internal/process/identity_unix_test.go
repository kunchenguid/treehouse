//go:build !windows

package process

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func TestStartStampIsStableForALiveProcess(t *testing.T) {
	pid := int32(os.Getpid())
	first, err := StartStamp(pid)
	if err != nil || first == 0 {
		t.Fatalf("StartStamp(self) = %d, %v", first, err)
	}
	second, err := StartStamp(pid)
	if err != nil || second != first {
		t.Fatalf("second StartStamp(self) = %d, %v; want %d", second, err, first)
	}
}

func TestStartStampReportsMissingProcess(t *testing.T) {
	cmd := startSleeper(t)
	pid := int32(cmd.Process.Pid)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if _, err := StartStamp(pid); !errors.Is(err, ErrNoProcess) {
		t.Fatalf("StartStamp(exited) error = %v, want ErrNoProcess", err)
	}
}

func TestIdentityLiveness(t *testing.T) {
	t.Run("recorded process still running is alive", func(t *testing.T) {
		cmd := startSleeper(t)
		pid := int32(cmd.Process.Pid)
		stamp, err := StartStamp(pid)
		if err != nil {
			t.Fatal(err)
		}
		if got := IdentityLiveness(pid, stamp); got != LivenessAlive {
			t.Fatalf("liveness = %v, want alive", got)
		}
	})
	t.Run("recorded process that exited is gone", func(t *testing.T) {
		cmd := startSleeper(t)
		pid := int32(cmd.Process.Pid)
		stamp, err := StartStamp(pid)
		if err != nil {
			t.Fatal(err)
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if got := IdentityLiveness(pid, stamp); got != LivenessGone {
			t.Fatalf("liveness = %v, want gone", got)
		}
	})
	t.Run("PID now held by a process with another start is gone", func(t *testing.T) {
		cmd := startSleeper(t)
		pid := int32(cmd.Process.Pid)
		stamp, err := StartStamp(pid)
		if err != nil {
			t.Fatal(err)
		}
		if got := IdentityLiveness(pid, stamp-1); got != LivenessGone {
			t.Fatalf("liveness = %v, want gone", got)
		}
	})
	t.Run("an identity without a PID or start is unknown", func(t *testing.T) {
		self := int32(os.Getpid())
		stamp, err := StartStamp(self)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			pid   int32
			stamp int64
		}{{0, stamp}, {-1, stamp}, {self, 0}} {
			if got := IdentityLiveness(tc.pid, tc.stamp); got != LivenessUnknown {
				t.Fatalf("liveness(%d, %d) = %v, want unknown", tc.pid, tc.stamp, got)
			}
		}
	})
}

func TestVerifyHolder(t *testing.T) {
	t.Run("the caller's parent is accepted with its start", func(t *testing.T) {
		parent := int32(os.Getppid())
		stamp, err := VerifyHolder(parent)
		if err != nil {
			t.Fatalf("VerifyHolder(parent): %v", err)
		}
		want, err := StartStamp(parent)
		if err != nil || stamp != want {
			t.Fatalf("VerifyHolder(parent) stamp = %d, want %d (%v)", stamp, want, err)
		}
	})
	t.Run("the caller itself is refused", func(t *testing.T) {
		if _, err := VerifyHolder(int32(os.Getpid())); err == nil {
			t.Fatal("VerifyHolder(self) succeeded; a short-lived CLI must never be the holder")
		}
	})
	t.Run("a process outside the caller's ancestry is refused", func(t *testing.T) {
		cmd := startSleeper(t)
		if _, err := VerifyHolder(int32(cmd.Process.Pid)); err == nil {
			t.Fatal("VerifyHolder(child) succeeded")
		}
	})
	t.Run("an exited process is refused", func(t *testing.T) {
		cmd := startSleeper(t)
		pid := int32(cmd.Process.Pid)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if _, err := VerifyHolder(pid); err == nil {
			t.Fatal("VerifyHolder(exited) succeeded")
		}
	})
	t.Run("a non-positive PID is refused", func(t *testing.T) {
		for _, pid := range []int32{0, -1} {
			if _, err := VerifyHolder(pid); err == nil {
				t.Fatalf("VerifyHolder(%d) succeeded", pid)
			}
		}
	})
}
