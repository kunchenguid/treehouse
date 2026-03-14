package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v1.0.0", "v1.0.0", 0},
		{"v1.0.1", "v1.0.0", 1},
		{"v1.0.0", "v1.0.1", -1},
		{"v2.0.0", "v1.9.9", 1},
		{"v1.9.9", "v2.0.0", -1},
		{"v0.3.0", "v0.2.1", 1},
		{"v0.2.1", "v0.3.0", -1},
		// Without v prefix
		{"1.0.0", "1.0.0", 0},
		{"1.1.0", "1.0.0", 1},
		// Mixed prefix
		{"v1.0.0", "1.0.0", 0},
		// Partial versions
		{"v1", "v1.0.0", 0},
		{"v1.2", "v1.2.0", 0},
		// Pre-release sorts before release
		{"v1.0.0-beta", "v1.0.0", -1},
		{"v1.0.0", "v1.0.0-beta", 1},
		{"v1.0.0-alpha", "v1.0.0-beta", -1},
		{"v1.0.0-beta", "v1.0.0-alpha", 1},
		{"v1.0.0-rc.1", "v1.0.0-rc.1", 0},
		// Pre-release of higher version still beats release of lower
		{"v2.0.0-beta", "v1.0.0", 1},
	}

	for _, tt := range tests {
		got := CompareVersions(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input      string
		wantNums   [3]int
		wantPrerel string
	}{
		{"v1.2.3", [3]int{1, 2, 3}, ""},
		{"1.2.3", [3]int{1, 2, 3}, ""},
		{"v0.0.0", [3]int{0, 0, 0}, ""},
		{"v10.20.30", [3]int{10, 20, 30}, ""},
		{"v1", [3]int{1, 0, 0}, ""},
		{"v1.2", [3]int{1, 2, 0}, ""},
		{"v1.2.3-beta", [3]int{1, 2, 3}, "beta"},
		{"v1.2.3-rc.1", [3]int{1, 2, 3}, "rc.1"},
		{"invalid", [3]int{0, 0, 0}, ""},
	}

	for _, tt := range tests {
		got := parseVersion(tt.input)
		if got.nums != tt.wantNums {
			t.Errorf("parseVersion(%q).nums = %v, want %v", tt.input, got.nums, tt.wantNums)
		}
		if got.prerelease != tt.wantPrerel {
			t.Errorf("parseVersion(%q).prerelease = %q, want %q", tt.input, got.prerelease, tt.wantPrerel)
		}
	}
}

func TestCacheReadWriteStaleness(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", tmp)
	}

	// Cache should not exist yet
	result := ReadCache("v1.0.0")
	if result != nil {
		t.Fatal("expected nil from ReadCache with no cache file")
	}

	if !IsCacheStale() {
		t.Fatal("expected stale with no cache file")
	}

	// Write cache
	entry := CacheEntry{
		CheckedAt:     time.Now(),
		LatestVersion: "v2.0.0",
	}
	if err := writeCache(entry); err != nil {
		t.Fatalf("writeCache: %v", err)
	}

	// Cache should now be readable
	result = ReadCache("v1.0.0")
	if result == nil {
		t.Fatal("expected non-nil from ReadCache")
	}
	if !result.UpdateAvailable {
		t.Error("expected UpdateAvailable=true")
	}
	if result.LatestVersion != "v2.0.0" {
		t.Errorf("LatestVersion = %q, want %q", result.LatestVersion, "v2.0.0")
	}

	// Cache should not be stale
	if IsCacheStale() {
		t.Error("expected fresh cache")
	}

	// No update available when current >= latest
	result = ReadCache("v2.0.0")
	if result == nil {
		t.Fatal("expected non-nil from ReadCache")
	}
	if result.UpdateAvailable {
		t.Error("expected UpdateAvailable=false when current == latest")
	}

	result = ReadCache("v3.0.0")
	if result == nil {
		t.Fatal("expected non-nil from ReadCache")
	}
	if result.UpdateAvailable {
		t.Error("expected UpdateAvailable=false when current > latest")
	}
}

func TestCacheStaleAfterTTL(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", tmp)
	}

	entry := CacheEntry{
		CheckedAt:     time.Now().Add(-25 * time.Hour),
		LatestVersion: "v1.0.0",
	}
	if err := writeCache(entry); err != nil {
		t.Fatalf("writeCache: %v", err)
	}

	if !IsCacheStale() {
		t.Error("expected stale cache after TTL")
	}
}

func TestCacheCorrupted(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", tmp)
	}

	dir := filepath.Join(tmp, treehouseDir)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, cacheFileName), []byte("not json"), 0o644)

	result := ReadCache("v1.0.0")
	if result != nil {
		t.Error("expected nil from corrupted cache")
	}
}

func TestAssetNameForVersion(t *testing.T) {
	name := AssetNameForVersion("v1.2.3")
	if runtime.GOOS == "windows" {
		expected := "treehouse-v1.2.3-windows-" + runtime.GOARCH + ".zip"
		if name != expected {
			t.Errorf("AssetNameForVersion() = %q, want %q", name, expected)
		}
	} else {
		expected := "treehouse-v1.2.3-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
		if name != expected {
			t.Errorf("AssetNameForVersion() = %q, want %q", name, expected)
		}
	}
}

func TestMatchesCurrentPlatformAsset(t *testing.T) {
	name := AssetNameForVersion("v2.0.0")
	if !matchesCurrentPlatformAsset(name) {
		t.Errorf("expected %q to match current platform", name)
	}

	if matchesCurrentPlatformAsset("treehouse-v1.0.0-fakeos-fakearg.tar.gz") {
		t.Error("expected non-matching asset to fail")
	}
}

func TestExtractTarGz(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.tar.gz")
	content := []byte("#!/bin/sh\necho hello\n")

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	if err := tw.WriteHeader(&tar.Header{
		Name: "treehouse",
		Size: int64(len(content)),
		Mode: 0o755,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	f.Close()

	binaryPath, err := extractTarGz(archivePath)
	if err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	defer os.Remove(binaryPath)

	got, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("extracted content = %q, want %q", string(got), string(content))
	}
}

func TestExtractZip(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.zip")
	content := []byte("MZ fake exe")

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("treehouse.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	f.Close()

	binaryPath, err := extractZip(archivePath)
	if err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	defer os.Remove(binaryPath)

	got, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("extracted content = %q, want %q", string(got), string(content))
	}
}

func TestExtractTarGzMissingBinary(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "test.tar.gz")
	content := []byte("some file")

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{
		Name: "other-file",
		Size: int64(len(content)),
		Mode: 0o644,
	})
	tw.Write(content)
	tw.Close()
	gz.Close()
	f.Close()

	_, err = extractTarGz(archivePath)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestCacheRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", tmp)
	}

	now := time.Now().Truncate(time.Second)
	entry := CacheEntry{
		CheckedAt:     now,
		LatestVersion: "v1.2.3",
	}
	if err := writeCache(entry); err != nil {
		t.Fatalf("writeCache: %v", err)
	}

	data, err := os.ReadFile(cachePath())
	if err != nil {
		t.Fatal(err)
	}

	var got CacheEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal cache: %v", err)
	}

	if got.LatestVersion != "v1.2.3" {
		t.Errorf("LatestVersion = %q, want %q", got.LatestVersion, "v1.2.3")
	}
}

// createTestTarGz builds a tar.gz archive containing a fake "treehouse" binary
// with the given content, and returns the raw bytes.
func createTestTarGz(t *testing.T, content []byte) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{
		Name: "treehouse",
		Size: int64(len(content)),
		Mode: 0o755,
	})
	tw.Write(content)
	tw.Close()
	gz.Close()
	f.Close()
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return buf
}

func createTestZip(t *testing.T, content []byte) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("treehouse.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	f.Close()
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return buf
}

// createTestArchive creates a tar.gz on unix or zip on windows.
func createTestArchive(t *testing.T, content []byte) []byte {
	t.Helper()
	if runtime.GOOS == "windows" {
		return createTestZip(t, content)
	}
	return createTestTarGz(t, content)
}

// fakeGitHubServer starts an httptest server that serves a GitHub-like
// /releases/latest response, an asset download endpoint, and a checksums endpoint.
func fakeGitHubServer(t *testing.T, latestTag string, archiveBytes []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	assetName := AssetNameForVersion(latestTag)

	// Compute SHA256 checksum of the archive
	h := sha256.New()
	h.Write(archiveBytes)
	checksum := hex.EncodeToString(h.Sum(nil))
	checksumsContent := fmt.Sprintf("%s  %s\n", checksum, assetName)

	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		release := githubRelease{
			TagName: latestTag,
			Assets: []githubAsset{
				{
					Name:               assetName,
					BrowserDownloadURL: fmt.Sprintf("http://%s/download/%s", r.Host, assetName),
				},
				{
					Name:               "checksums.txt",
					BrowserDownloadURL: fmt.Sprintf("http://%s/download/checksums.txt", r.Host),
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(release)
	})

	mux.HandleFunc("/download/"+assetName, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(archiveBytes)
	})

	mux.HandleFunc("/download/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(checksumsContent))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Disable HTTPS enforcement for test HTTP servers
	enforceHTTPS = false
	t.Cleanup(func() { enforceHTTPS = true })

	return srv
}

func TestCheckLatestE2E(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", tmp)
	}

	archive := createTestArchive(t, []byte("new-binary-content"))
	srv := fakeGitHubServer(t, "v2.0.0", archive)

	origURL := githubAPIURL
	githubAPIURL = srv.URL + "/releases/latest"
	t.Cleanup(func() { githubAPIURL = origURL })

	result, err := CheckLatest("v1.0.0")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}

	if !result.UpdateAvailable {
		t.Error("expected UpdateAvailable=true")
	}
	if result.LatestVersion != "v2.0.0" {
		t.Errorf("LatestVersion = %q, want %q", result.LatestVersion, "v2.0.0")
	}
	if result.DownloadURL == "" {
		t.Error("expected DownloadURL to be set")
	}
	if result.ChecksumURL == "" {
		t.Error("expected ChecksumURL to be set")
	}

	// Verify cache was written
	cached := ReadCache("v1.0.0")
	if cached == nil {
		t.Fatal("expected cache to be written")
	}
	if cached.LatestVersion != "v2.0.0" {
		t.Errorf("cached LatestVersion = %q, want %q", cached.LatestVersion, "v2.0.0")
	}
	if IsCacheStale() {
		t.Error("cache should be fresh after CheckLatest")
	}
}

func TestCheckLatestNoUpdate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", tmp)
	}

	archive := createTestArchive(t, []byte("content"))
	srv := fakeGitHubServer(t, "v1.0.0", archive)

	origURL := githubAPIURL
	githubAPIURL = srv.URL + "/releases/latest"
	t.Cleanup(func() { githubAPIURL = origURL })

	result, err := CheckLatest("v1.0.0")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}

	if result.UpdateAvailable {
		t.Error("expected UpdateAvailable=false when versions match")
	}
}

func TestApplyE2E(t *testing.T) {
	newContent := []byte("#!/bin/sh\necho updated\n")
	archive := createTestArchive(t, newContent)
	srv := fakeGitHubServer(t, "v2.0.0", archive)

	// Create a fake "current binary" to be replaced
	targetDir := t.TempDir()
	binaryName := "treehouse"
	if runtime.GOOS == "windows" {
		binaryName = "treehouse.exe"
	}
	targetPath := filepath.Join(targetDir, binaryName)
	if err := os.WriteFile(targetPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	assetName := AssetNameForVersion("v2.0.0")
	result := &CheckResult{
		CurrentVersion:  "v1.0.0",
		LatestVersion:   "v2.0.0",
		UpdateAvailable: true,
		DownloadURL:     srv.URL + "/download/" + assetName,
		ChecksumURL:     srv.URL + "/download/checksums.txt",
	}

	// Test individual steps since we can't patch os.Executable
	archivePath, err := downloadToTemp(result.DownloadURL)
	if err != nil {
		t.Fatalf("downloadToTemp: %v", err)
	}
	defer os.Remove(archivePath)

	if err := verifyChecksum(archivePath, result.ChecksumURL); err != nil {
		t.Fatalf("verifyChecksum: %v", err)
	}

	newBinaryPath, err := extractBinary(archivePath)
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	defer os.Remove(newBinaryPath)

	got, err := os.ReadFile(newBinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newContent) {
		t.Errorf("extracted = %q, want %q", string(got), string(newContent))
	}

	if err := atomicReplace(targetPath, newBinaryPath); err != nil {
		t.Fatalf("atomicReplace: %v", err)
	}

	replaced, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(replaced) != string(newContent) {
		t.Errorf("replaced binary = %q, want %q", string(replaced), string(newContent))
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("expected executable permissions on replaced binary")
	}
}

func TestRequireHTTPS(t *testing.T) {
	if err := requireHTTPS("https://example.com/file"); err != nil {
		t.Errorf("expected no error for HTTPS URL, got: %v", err)
	}
	if err := requireHTTPS("http://example.com/file"); err == nil {
		t.Error("expected error for HTTP URL")
	}
	if err := requireHTTPS(""); err == nil {
		t.Error("expected error for empty URL")
	}
}

func TestVerifyChecksumMismatch(t *testing.T) {
	tmp, err := os.CreateTemp("", "checksum-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	tmp.Write([]byte("real content"))
	tmp.Close()

	assetName := AssetNameForVersion("v1.0.0")
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	mux := http.NewServeMux()
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", wrongHash, assetName)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	enforceHTTPS = false
	defer func() { enforceHTTPS = true }()

	err = verifyChecksum(tmp.Name(), srv.URL+"/checksums.txt")
	if err == nil {
		t.Fatal("expected error for checksum mismatch")
	}
}

func TestVerifyChecksumNoURL(t *testing.T) {
	err := verifyChecksum("/dev/null", "")
	if err == nil {
		t.Fatal("expected error when checksum URL is empty")
	}
}
