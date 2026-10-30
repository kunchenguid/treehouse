package gitvcs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsDirtyDoesNotEnumerateUntrackedDescendants(t *testing.T) {
	repo := initRepo(t)
	for i := range 64 {
		writeTestFile(t, repo, filepath.Join("scratch", "nested", fmt.Sprintf("file-%d", i)), "untracked\n")
	}
	trace := filepath.Join(t.TempDir(), "trace.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	if dirty, err := IsDirty(repo); err != nil || !dirty {
		t.Fatalf("nested untracked files: dirty=%v err=%v", dirty, err)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event struct {
			Category string          `json:"category"`
			Key      string          `json:"key"`
			Value    json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Category == "status" && event.Key == "count/untracked" {
			if string(event.Value) != `"1"` {
				t.Fatalf("boolean dirty check enumerated %s untracked entries; one directory entry suffices", event.Value)
			}
			return
		}
	}
	t.Fatal("git trace did not report the untracked entry count")
}
