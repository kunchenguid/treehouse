package cmd

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/v3/internal/config"
	"github.com/kunchenguid/treehouse/v3/internal/pool"
)

// TestMaxTreesAdviceKeepsPoolFullMatchable pins the errors.Is contract that
// callers rely on: a real allocator pool-full error matches pool.ErrPoolFull,
// and still matches after get appends the max_trees advice. Formatting the
// advice with %v instead of %w would keep the text but break this match.
func TestMaxTreesAdviceKeepsPoolFullMatchable(t *testing.T) {
	repoDir, _ := setupTestRepo(t)
	poolDir := filepath.Join(t.TempDir(), "pool")

	if _, err := pool.AcquireLease(repoDir, poolDir, 1, nil, "first"); err != nil {
		t.Fatalf("first lease: %v", err)
	}
	_, full := pool.AcquireLease(repoDir, poolDir, 1, nil, "second")
	if !errors.Is(full, pool.ErrPoolFull) {
		t.Fatalf("allocator error at the cap must match ErrPoolFull, got %v", full)
	}

	repoCfg := filepath.Join(repoDir, "treehouse.toml")
	cfg := config.Config{MaxTrees: 1, MaxTreesSource: config.MaxTreesSource{Origin: config.MaxTreesRepo, File: repoCfg}}
	advised := withMaxTreesAdvice(full, repoDir, cfg)
	if !errors.Is(advised, pool.ErrPoolFull) {
		t.Fatalf("advised error must still match ErrPoolFull, got %v", advised)
	}
	if !strings.HasPrefix(advised.Error(), full.Error()+"\n") {
		t.Fatalf("advice must follow the unchanged allocator message, got %q", advised.Error())
	}
}

// TestMaxTreesAdvicePassesOtherErrorsThrough checks that a failure unrelated
// to the pool cap is returned as the identical error value, with no advice.
func TestMaxTreesAdvicePassesOtherErrorsThrough(t *testing.T) {
	other := errors.New("fetch failed")
	cfg := config.Config{MaxTrees: 1, MaxTreesSource: config.MaxTreesSource{Origin: config.MaxTreesDefault}}
	if got := withMaxTreesAdvice(other, t.TempDir(), cfg); got != other {
		t.Fatalf("unrelated error must pass through unchanged, got %v", got)
	}
}
