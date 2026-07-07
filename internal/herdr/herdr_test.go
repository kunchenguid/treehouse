package herdr

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestIsRuntime(t *testing.T) {
	t.Setenv(RuntimeEnvVar, "1")
	if !IsRuntime() {
		t.Fatal("IsRuntime() = false with HERDR_ENV=1, want true")
	}
	t.Setenv(RuntimeEnvVar, "0")
	if IsRuntime() {
		t.Fatal("IsRuntime() = true with HERDR_ENV=0, want false")
	}
	t.Setenv(RuntimeEnvVar, "")
	if IsRuntime() {
		t.Fatal("IsRuntime() = true with HERDR_ENV unset, want false")
	}
}

func TestDisabled(t *testing.T) {
	t.Setenv(DisableEnvVar, "1")
	if !Disabled() {
		t.Fatal("Disabled() = false with TREEHOUSE_NO_HERDR=1, want true")
	}
	t.Setenv(DisableEnvVar, "")
	if Disabled() {
		t.Fatal("Disabled() = true with TREEHOUSE_NO_HERDR unset, want false")
	}
}

func TestAvailable(t *testing.T) {
	orig := binary
	t.Cleanup(func() { binary = orig })

	// "go" is guaranteed present while running the test suite.
	binary = "go"
	if !Available() {
		t.Fatal("Available() = false for an on-PATH binary, want true")
	}
	binary = "treehouse-definitely-not-on-path-xyz"
	if Available() {
		t.Fatal("Available() = true for a missing binary, want false")
	}
}

func TestBuildStartArgs(t *testing.T) {
	got := buildStartArgs(SpawnOptions{
		Exe:          "/usr/local/bin/treehouse",
		WorktreePath: "/home/u/.treehouse/proj-abc/1/proj",
		PoolDir:      "/home/u/.treehouse/proj-abc",
		Label:        "proj:1",
		Split:        "right",
		Focus:        true,
	})
	want := []string{
		"agent", "start", "proj:1",
		"--cwd", "/home/u/.treehouse/proj-abc/1/proj",
		"--split", "right",
		"--focus",
		"--", "/usr/local/bin/treehouse", "__hold",
		"/home/u/.treehouse/proj-abc/1/proj", "/home/u/.treehouse/proj-abc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildStartArgs focus/right mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestBuildStartArgs_NoFocusDownDefault(t *testing.T) {
	got := buildStartArgs(SpawnOptions{
		Exe:          "th",
		WorktreePath: "/wt",
		PoolDir:      "/pool",
		Label:        "l",
		Split:        "weird-value", // anything not "down" must default to "right"
		Focus:        false,
	})
	if !contains(got, "--no-focus") || contains(got, "--focus") {
		t.Fatalf("expected --no-focus only, got %v", got)
	}
	if idx := indexOf(got, "--split"); idx < 0 || got[idx+1] != "right" {
		t.Fatalf("expected split to default to right, got %v", got)
	}

	down := buildStartArgs(SpawnOptions{Split: "down", WorktreePath: "/wt", PoolDir: "/pool", Exe: "th", Label: "l"})
	if idx := indexOf(down, "--split"); idx < 0 || down[idx+1] != "down" {
		t.Fatalf("expected split=down, got %v", down)
	}
	// The holder argv must always be the trailing segment after "--".
	if idx := indexOf(got, "--"); idx < 0 || got[idx+1] != "th" || got[idx+2] != HoldSubcommand {
		t.Fatalf("expected holder argv after --, got %v", got)
	}
}

func TestParsePaneID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"nested result.terminal.pane_id", `{"result":{"terminal":{"pane_id":"1-3"}}}`, "1-3"},
		{"nested result.pane.pane_id", `{"result":{"pane":{"pane_id":"2-1"}}}`, "2-1"},
		{"nested result.terminal.id", `{"result":{"terminal":{"id":"t9"}}}`, "t9"},
		{"flat result.pane_id", `{"result":{"pane_id":"4-2"}}`, "4-2"},
		{"top-level pane_id", `{"pane_id":"5-5"}`, "5-5"},
		{"agent object", `{"result":{"agent":{"terminal_id":"7-1"}}}`, "7-1"},
		{"no id", `{"result":{"ok":true}}`, ""},
		{"garbage", `not json`, ""},
		{"empty", ``, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parsePaneID([]byte(tc.in)); got != tc.want {
				t.Fatalf("parsePaneID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRoutingMessageMentionsSkill(t *testing.T) {
	msg := RoutingMessage()
	if !strings.Contains(msg, "/herdr") {
		t.Fatalf("routing message should point at the /herdr skill, got %q", msg)
	}
	if strings.Contains(msg, "—") {
		t.Fatalf("routing message must not use an em dash, got %q", msg)
	}
}

func TestSpawnHoldTimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a /bin/sh holder stub to stand in for an unresponsive herdr")
	}
	origBin, origTimeout := binary, spawnTimeout
	t.Cleanup(func() { binary = origBin; spawnTimeout = origTimeout })

	// A stub that ignores its args and blocks well past the timeout, standing in
	// for a herdr server whose `agent start` never returns. `exec`-ing sleep lets
	// the context's Kill reach the blocking process directly instead of orphaning
	// it behind the shell.
	stub := filepath.Join(t.TempDir(), "slow-herdr")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	binary = stub
	spawnTimeout = 150 * time.Millisecond

	start := time.Now()
	_, err := SpawnHold(SpawnOptions{Exe: "th", WorktreePath: "/wt", PoolDir: "/pool", Label: "l"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("SpawnHold returned a nil error for an unresponsive herdr, want a timeout error so get falls back")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("SpawnHold error = %q, want it to mention the timeout", err)
	}
	// It must abandon near the timeout, not block until the stub's own 30s sleep.
	if elapsed > 5*time.Second {
		t.Fatalf("SpawnHold blocked for %s, want it to give up near the %s timeout", elapsed, spawnTimeout)
	}
}

func contains(s []string, v string) bool { return indexOf(s, v) >= 0 }

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
