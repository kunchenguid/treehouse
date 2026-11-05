package cmd

import (
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestResolveGetTimeout_FlagWins(t *testing.T) {
	cmd := &cobra.Command{Use: "get"}
	cmd.Flags().DurationVar(&getTimeout, "timeout", defaultGetTimeout, "")
	getTimeout = defaultGetTimeout
	if err := cmd.Flags().Set("timeout", "45s"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TREEHOUSE_GET_TIMEOUT", "10s")
	got, err := resolveGetTimeout(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if got != 45*time.Second {
		t.Fatalf("got %v, want 45s", got)
	}
}

func TestResolveGetTimeout_EnvOverridesDefault(t *testing.T) {
	cmd := &cobra.Command{Use: "get"}
	cmd.Flags().DurationVar(&getTimeout, "timeout", defaultGetTimeout, "")
	getTimeout = defaultGetTimeout
	t.Setenv("TREEHOUSE_GET_TIMEOUT", "90s")
	got, err := resolveGetTimeout(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if got != 90*time.Second {
		t.Fatalf("got %v, want 90s", got)
	}
}

func TestResolveGetTimeout_Default(t *testing.T) {
	cmd := &cobra.Command{Use: "get"}
	cmd.Flags().DurationVar(&getTimeout, "timeout", defaultGetTimeout, "")
	getTimeout = defaultGetTimeout
	t.Setenv("TREEHOUSE_GET_TIMEOUT", "")
	got, err := resolveGetTimeout(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if got != defaultGetTimeout {
		t.Fatalf("got %v, want %v", got, defaultGetTimeout)
	}
}

func TestResolveGetTimeout_MalformedEnv(t *testing.T) {
	cmd := &cobra.Command{Use: "get"}
	cmd.Flags().DurationVar(&getTimeout, "timeout", defaultGetTimeout, "")
	t.Setenv("TREEHOUSE_GET_TIMEOUT", "not-a-duration")
	_, err := resolveGetTimeout(cmd)
	if err == nil {
		t.Fatal("expected error for malformed TREEHOUSE_GET_TIMEOUT")
	}
}
