package main

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// INV-1 (.github/workflows/ci.yml): Windows partitions retain coverage and Go's watchdog.
func TestCIWindowsTestPartitions(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Env   map[string]string `yaml:"env"`
			Steps []struct {
				Name string `yaml:"name"`
				If   string `yaml:"if"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	job := workflow.Jobs["test"]
	if got, want := job.Env["WORKSPACE_TESTS"], "^Test(Workspace|RemoveWorkspaceRepository|ResolveWorktreePathResolvesFacadeLink|ReturnOutsideRepository)"; got != want {
		t.Fatalf("workspace partition = %q, want %q", got, want)
	}
	for _, expected := range []struct {
		name, condition, command string
	}{
		{"Test", "matrix.os != 'windows-latest'", "go test ./..."},
		{"Test Windows core", "matrix.os == 'windows-latest'", "go test -p 1 ./... -skip \"$env:WORKSPACE_TESTS\""},
		{"Test Windows workspaces", "matrix.os == 'windows-latest'", "go test -p 1 ./... -run \"$env:WORKSPACE_TESTS\""},
	} {
		found := false
		for _, step := range job.Steps {
			if step.Name != expected.name {
				continue
			}
			found = true
			if step.If != expected.condition || step.Run != expected.command {
				t.Errorf("%s: if=%q run=%q, want if=%q run=%q", step.Name, step.If, step.Run, expected.condition, expected.command)
			}
		}
		if !found {
			t.Errorf("missing %s step", expected.name)
		}
	}
}
