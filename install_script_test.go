package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Exercise the installer through sh, stopping at the asset request: no network,
// archive extraction, installation, or sudo is needed to test version resolution.
func TestInstallScriptLatestRedirect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell installer")
	}
	const releaseURL = "https://github.com/kunchenguid/treehouse/releases/"
	for _, tc := range []struct {
		name, effectiveURL, version string
		curlFailure                 bool
	}{
		{name: "versioned tag", effectiveURL: releaseURL + "tag/v3.1.5", version: "v3.1.5"},
		{name: "URL suffixes", effectiveURL: releaseURL + "tag/v3.1.5/?source=latest#asset", version: "v3.1.5"},
		{name: "unprefixed tag", effectiveURL: releaseURL + "tag/3.1.5", version: "3.1.5"},
		{name: "empty result"},
		{name: "empty tag", effectiveURL: releaseURL + "tag/"},
		{name: "no tag redirect", effectiveURL: releaseURL + "latest"},
		{name: "HTTP failure", effectiveURL: releaseURL + "tag/v3.1.5", curlFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(dir, "curl.log")
			commands := map[string]string{
				"uname": "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; -m) echo x86_64 ;; *) exit 99 ;; esac\n",
				"curl": `#!/bin/sh
printf '%s\n' "$*" >> "$CURL_LOG"
if [ "$*" = '-fsSLI -o /dev/null -w %{url_effective} https://github.com/kunchenguid/treehouse/releases/latest' ]; then
  [ "$CURL_FAILURE" = false ] || exit 22
  printf '%s' "$EFFECTIVE_URL"
  exit 0
fi
# Refuse every other request, including the asset: never perform a real install.
exit 42
`,
			}
			for name, script := range commands {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", dir)
			t.Setenv("TMPDIR", dir)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CURL_LOG", log)
			t.Setenv("EFFECTIVE_URL", tc.effectiveURL)
			failure := "false"
			if tc.curlFailure {
				failure = "true"
			}
			t.Setenv("CURL_FAILURE", failure)
			cmd := exec.Command("sh", filepath.Join("docs", "install.sh"))
			output, err := cmd.CombinedOutput()
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("expected controlled refusal, got %v: %s", err, output)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			requests := strings.Split(strings.TrimSpace(string(data)), "\n")
			if strings.Contains(string(data), "api.github.com") {
				t.Fatalf("must not use the rate-limited API: %s", data)
			}
			if tc.version == "" {
				if len(requests) != 1 || strings.Contains(string(output), "Downloading") {
					t.Fatalf("unresolved version must not request an asset: %s\n%s", data, output)
				}
				wantExit := 1
				if tc.curlFailure {
					wantExit = 22
				} else if !strings.Contains(string(output), "Failed to determine latest version") {
					t.Fatalf("missing empty-version diagnostic: %s", output)
				}
				if exitErr.ExitCode() != wantExit {
					t.Fatalf("exit = %d, want %d: %s", exitErr.ExitCode(), wantExit, output)
				}
				return
			}
			asset := releaseURL + "download/" + tc.version + "/treehouse-v" + strings.TrimPrefix(tc.version, "v") + "-linux-amd64.tar.gz"
			if exitErr.ExitCode() != 42 || len(requests) != 2 || !strings.HasPrefix(requests[1], "-fsSL "+asset+" -o ") {
				t.Fatalf("expected versioned asset request %s, got exit %d:\n%s\n%s", asset, exitErr.ExitCode(), data, output)
			}
		})
	}
}
