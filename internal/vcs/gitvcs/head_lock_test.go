package gitvcs

import (
	"errors"
	"os"
	"testing"
)

func TestHeadLockPreservesLiveTreehouseOwner(t *testing.T) {
	slot, target, head := setupSafeResetWorktree(t)
	headPath, err := gitPath(slot, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	f, release, err := openHeadLock(slot, headPath)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !sameRegularFile(headPath+".lock", headPath+".treehouse-witness") {
		t.Skip("filesystem cannot create ownership witness hard links")
	}
	identity, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []struct {
		name string
		run  func() error
	}{
		{"reset", func() error { return ResetWorktreeToRef(slot, target, head, false) }},
		{"detach", func() error { return DetachWorktree(slot) }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); !errors.Is(err, ErrHeadLocked) {
				t.Fatalf("live owner's guard must prevent recovery: %v", err)
			}
			current, err := os.Stat(headPath + ".lock")
			if err != nil || !os.SameFile(identity, current) {
				t.Fatalf("live owner's HEAD.lock changed: %v", err)
			}
			if got, err := worktreeHead(slot); err != nil || got != head {
				t.Fatalf("live owner's HEAD changed: %s (%v)", got, err)
			}
		})
	}
}

func TestHeadLockRejectsUnprovenWitness(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "different file", true: "symlink"}[symlink], func(t *testing.T) {
			slot, target, head := setupSafeResetWorktree(t)
			headPath, err := gitPath(slot, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			lock, witness := headPath+".lock", headPath+".treehouse-witness"
			if err := os.WriteFile(lock, []byte("keep\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if symlink {
				if err := os.Symlink(lock, witness); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			} else if err := os.WriteFile(witness, []byte("keep\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := ResetWorktreeToRef(slot, target, head, false); !errors.Is(err, ErrHeadLocked) {
				t.Fatalf("unproven witness must prevent recovery: %v", err)
			}
			if data, err := os.ReadFile(lock); err != nil || string(data) != "keep\n" {
				t.Fatalf("anonymous HEAD.lock changed: %q (%v)", data, err)
			}
		})
	}
}

func TestHeadLockRecoversOrphanWitness(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "before lock publication", true: "after HEAD publication"}[published], func(t *testing.T) {
			slot, target, head := setupSafeResetWorktree(t)
			headPath, err := gitPath(slot, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			witness := headPath + ".treehouse-witness"
			if published {
				if err := os.Link(headPath, witness); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
			} else if err := os.WriteFile(witness, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := ResetWorktreeToRef(slot, target, head, true); err != nil {
				t.Fatalf("orphan witness must not poison the next reset: %v", err)
			}
			if _, err := os.Stat(witness); !os.IsNotExist(err) {
				t.Fatalf("reset left its witness: %v", err)
			}
			if got, err := worktreeHead(slot); err != nil || got != target {
				t.Fatalf("HEAD = %s, want %s (%v)", got, target, err)
			}
		})
	}
}
