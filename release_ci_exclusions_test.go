package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// releasePleaseConfig is the subset of release-please-config.json we need
// to derive the files a release PR is expected to touch.
type releasePleaseConfig struct {
	Packages map[string]releasePleasePackage `json:"packages"`
}

type releasePleasePackage struct {
	ReleaseType string   `json:"release-type"`
	ExtraFiles  []string `json:"extra-files"`
}

// expectedReleaseOutputs derives the complete set of paths release-please
// writes for this repository from release-please-config.json. The set is
// the source of truth for pull_request path filters: every PR-triggered
// workflow must exclude every path here, or release PRs start creating
// action_required runs again.
func expectedReleaseOutputs(cfg releasePleaseConfig) ([]string, error) {
	if len(cfg.Packages) == 0 {
		return nil, fmt.Errorf("release-please-config.json has no packages")
	}

	seen := map[string]struct{}{
		// Manifest is always written for multi and single-package configs.
		".release-please-manifest.json": {},
	}

	for pkgPath, pkg := range cfg.Packages {
		// CHANGELOG.md lives at the package root (repo root for ".").
		changelog := "CHANGELOG.md"
		if pkgPath != "." && pkgPath != "" {
			changelog = filepath.ToSlash(filepath.Join(pkgPath, "CHANGELOG.md"))
		}

		switch pkg.ReleaseType {
		case "go":
			seen[changelog] = struct{}{}
		case "simple":
			seen[changelog] = struct{}{}
			versionTxt := "version.txt"
			if pkgPath != "." && pkgPath != "" {
				versionTxt = filepath.ToSlash(filepath.Join(pkgPath, "version.txt"))
			}
			seen[versionTxt] = struct{}{}
		case "node":
			seen[changelog] = struct{}{}
			packageJSON := "package.json"
			if pkgPath != "." && pkgPath != "" {
				packageJSON = filepath.ToSlash(filepath.Join(pkgPath, "package.json"))
			}
			seen[packageJSON] = struct{}{}
			// Only include package-lock.json when the repo actually has one.
			// pnpm repositories keep a root pnpm-lock.yaml that release-please
			// does not write.
			lock := "package-lock.json"
			if pkgPath != "." && pkgPath != "" {
				lock = filepath.ToSlash(filepath.Join(pkgPath, "package-lock.json"))
			}
			if _, err := os.Stat(lock); err == nil {
				seen[lock] = struct{}{}
			}
		case "":
			return nil, fmt.Errorf("package %q missing release-type", pkgPath)
		default:
			// Unknown types still produce a CHANGELOG at the package root;
			// fail closed by requiring at least that plus extra-files.
			seen[changelog] = struct{}{}
		}

		for _, extra := range pkg.ExtraFiles {
			if extra == "" {
				continue
			}
			seen[filepath.ToSlash(extra)] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func parseWorkflowOn(data []byte) (map[string]any, error) {
	// Unmarshal into map[any]any so a bare `on:` key (YAML 1.1 boolean
	// true) is preserved as bool true rather than being stringified away.
	var raw map[any]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	for _, key := range []any{"on", true} {
		v, ok := raw[key]
		if !ok {
			continue
		}
		return asStringKeyMap(v)
	}
	return nil, fmt.Errorf("workflow has no on: trigger map")
}

func asStringKeyMap(v any) (map[string]any, error) {
	switch typed := v.(type) {
	case map[string]any:
		return typed, nil
	case map[any]any:
		out := make(map[string]any, len(typed))
		for k, val := range typed {
			out[fmt.Sprint(k)] = val
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unexpected on: value type %T", v)
	}
}

// pathFilter describes how a pull_request trigger filters by path.
type pathFilter struct {
	// kind is "none", "ignore", or "allow".
	kind string
	// patterns are the paths-ignore entries (kind=ignore) or paths entries
	// (kind=allow). Negative allow patterns start with '!'.
	patterns []string
}

func pullRequestPathFilter(on map[string]any) (pathFilter, bool, error) {
	prRaw, ok := on["pull_request"]
	if !ok {
		return pathFilter{}, false, nil
	}

	// Bare `pull_request:` (null / nil) means "all PRs, no path filter".
	if prRaw == nil {
		return pathFilter{kind: "none"}, true, nil
	}

	pr, err := asStringKeyMap(prRaw)
	if err != nil {
		// `pull_request: [opened, synchronize]` list form - no path filter.
		return pathFilter{kind: "none"}, true, nil
	}

	ignore, hasIgnore, err := stringListField(pr, "paths-ignore")
	if err != nil {
		return pathFilter{}, true, err
	}
	allow, hasAllow, err := stringListField(pr, "paths")
	if err != nil {
		return pathFilter{}, true, err
	}
	if hasIgnore && hasAllow {
		return pathFilter{}, true, fmt.Errorf("pull_request sets both paths and paths-ignore")
	}
	if hasIgnore {
		return pathFilter{kind: "ignore", patterns: ignore}, true, nil
	}
	if hasAllow {
		return pathFilter{kind: "allow", patterns: allow}, true, nil
	}
	return pathFilter{kind: "none"}, true, nil
}

func stringListField(m map[string]any, key string) ([]string, bool, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return nil, false, nil
	}
	switch typed := v.(type) {
	case []any:
		out := make([]string, 0, len(typed))
		for i, item := range typed {
			s, ok := item.(string)
			if !ok {
				return nil, true, fmt.Errorf("%s[%d] is %T, want string", key, i, item)
			}
			out = append(out, s)
		}
		return out, true, nil
	case []string:
		return append([]string(nil), typed...), true, nil
	default:
		return nil, true, fmt.Errorf("%s has type %T, want list", key, v)
	}
}

// pathExcluded reports whether path is excluded by the filter, matching
// GitHub's documented paths / paths-ignore semantics closely enough for
// exact file paths (no glob expansion beyond "**" / "*" prefix/suffix forms
// used in this fleet). Exact string matches always work; for allow-lists,
// a later negative pattern ("!path") re-excludes after a positive match.
func pathExcluded(filter pathFilter, path string) bool {
	switch filter.kind {
	case "none":
		return false
	case "ignore":
		for _, pat := range filter.patterns {
			if matchGitHubPath(pat, path) {
				return true
			}
		}
		return false
	case "allow":
		// GitHub: workflow runs only if at least one path matches a positive
		// pattern and is not excluded by a later negative pattern.
		// We only need "would this single release-output path cause a run?".
		// It causes a run only if some positive pattern matches it and no
		// subsequent negative pattern excludes it.
		matched := false
		for _, pat := range filter.patterns {
			if strings.HasPrefix(pat, "!") {
				if matched && matchGitHubPath(strings.TrimPrefix(pat, "!"), path) {
					matched = false
				}
				continue
			}
			if matchGitHubPath(pat, path) {
				matched = true
			}
		}
		// Excluded when it does not survive as a positive match.
		return !matched
	default:
		return false
	}
}

// matchGitHubPath is a minimal matcher for the patterns this repository
// uses: exact paths, trailing /**, and simple * / ** globs.
func matchGitHubPath(pattern, path string) bool {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)
	if pattern == path {
		return true
	}
	// directory/**
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	// *.ext at any depth is not used here; support **/*.ext for root+nested.
	if strings.HasPrefix(pattern, "**/") {
		suf := strings.TrimPrefix(pattern, "**/")
		if path == suf || strings.HasSuffix(path, "/"+suf) {
			return true
		}
		// **/*.md style
		if strings.HasPrefix(suf, "*.") {
			ext := strings.TrimPrefix(suf, "*")
			if strings.HasSuffix(path, ext) {
				return true
			}
		}
	}
	if strings.HasPrefix(pattern, "*.") {
		return strings.HasSuffix(path, strings.TrimPrefix(pattern, "*"))
	}
	return false
}

// workflowJobNames returns every job `name:` declared by a workflow. A workflow
// that publishes a required status check is identified by these names, since a
// required check's context is the job name GitHub reports.
func workflowJobNames(data []byte) ([]string, error) {
	var wf struct {
		Jobs map[string]struct {
			Name string `yaml:"name"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(wf.Jobs))
	for _, job := range wf.Jobs {
		if job.Name != "" {
			names = append(names, job.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// publishesRequiredCheck reports whether a workflow backs a required status
// check on main. Such a workflow must NOT filter by path: a required check that
// never runs never reports, and the pull request waits on it forever. Release
// PRs are handled by the gate's own release-please exemption instead.
func publishesRequiredCheck(data []byte) (bool, error) {
	names, err := workflowJobNames(data)
	if err != nil {
		return false, err
	}
	for _, name := range names {
		if name == requiredCheckContext {
			return true, nil
		}
	}
	return false, nil
}

func TestPullRequestWorkflowsExcludeReleasePleaseOutputs(t *testing.T) {
	cfgBytes, err := os.ReadFile("release-please-config.json")
	if err != nil {
		t.Fatalf("read release-please-config.json: %v", err)
	}
	var cfg releasePleaseConfig
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		t.Fatalf("parse release-please-config.json: %v", err)
	}

	expected, err := expectedReleaseOutputs(cfg)
	if err != nil {
		t.Fatalf("derive release outputs: %v", err)
	}
	if len(expected) == 0 {
		t.Fatal("derived empty release output set")
	}
	t.Logf("expected release-please outputs: %v", expected)

	entries, err := os.ReadDir(filepath.Join(".github", "workflows"))
	if err != nil {
		t.Fatalf("read .github/workflows: %v", err)
	}

	var prWorkflows, requiredCheckWorkflows int
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		path := filepath.Join(".github", "workflows", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		on, err := parseWorkflowOn(data)
		if err != nil {
			t.Fatalf("parse on: in %s: %v", path, err)
		}
		filter, hasPR, err := pullRequestPathFilter(on)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !hasPR {
			continue
		}
		required, err := publishesRequiredCheck(data)
		if err != nil {
			t.Fatalf("parse jobs in %s: %v", path, err)
		}
		if required {
			requiredCheckWorkflows++
			t.Logf("%s backs the %q required check; path filters are forbidden there, not required", path, requiredCheckContext)
			if filter.kind != "none" {
				t.Errorf("%s backs a required check and must not filter by path (a filtered required check never reports)", path)
			}
			continue
		}
		prWorkflows++

		var missing []string
		for _, rel := range expected {
			if !pathExcluded(filter, rel) {
				missing = append(missing, rel)
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s pull_request filter must exclude every release-please output; missing: %s",
				path, strings.Join(missing, ", "))
		}
	}

	if prWorkflows == 0 {
		t.Fatal("no pull_request-triggered workflows found under .github/workflows")
	}
	// Exactly the no-mistakes gate is exempt today. A second exemption means a
	// new workflow claimed the required-check name, which needs a look.
	if requiredCheckWorkflows != 1 {
		t.Fatalf("expected exactly 1 pull_request workflow backing the %q required check, found %d",
			requiredCheckContext, requiredCheckWorkflows)
	}
}

func TestExpectedReleaseOutputsIncludesConfiguredExtraFiles(t *testing.T) {
	// Sanity: the derivation must surface flake.nix from this repo's config
	// so a future extra-files addition cannot silently drop out of the guard.
	cfgBytes, err := os.ReadFile("release-please-config.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg releasePleaseConfig
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		t.Fatal(err)
	}
	got, err := expectedReleaseOutputs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".release-please-manifest.json", "CHANGELOG.md", "flake.nix"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("expected %v, got %v", want, got)
	}
}
