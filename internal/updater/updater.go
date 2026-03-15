package updater

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultGitHubAPIURL = "https://api.github.com/repos/atinylittleshell/treehouse/releases/latest"
	cacheFileName       = "update-check.json"
	checksumsFile       = "checksums.txt"
	cacheTTL            = 24 * time.Hour
	httpTimeout         = 30 * time.Second
	maxDownloadSize     = 100 << 20 // 100 MB
	maxBinarySize       = 100 << 20 // 100 MB
	maxAPIResponseSize  = 5 << 20   // 5 MB
	treehouseDir        = ".treehouse"
)

// githubAPIURL is the endpoint for fetching the latest release.
// Overridden in tests to point at a local HTTP server.
var githubAPIURL = defaultGitHubAPIURL

// enforceHTTPS controls whether download URLs must use HTTPS.
// Disabled in tests where httptest servers use plain HTTP.
var enforceHTTPS = true

// CheckResult holds the outcome of a version check.
type CheckResult struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	DownloadURL     string `json:"download_url,omitempty"`
	ChecksumURL     string `json:"checksum_url,omitempty"`
}

// CacheEntry is persisted to ~/.treehouse/update-check.json.
type CacheEntry struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
}

// githubRelease is the subset of the GitHub API response we need.
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// CheckLatest hits the GitHub API, compares versions, and writes the cache.
func CheckLatest(currentVersion string) (*CheckResult, error) {
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Get(githubAPIURL)
	if err != nil {
		return nil, fmt.Errorf("fetching latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponseSize)).Decode(&release); err != nil {
		return nil, fmt.Errorf("decoding release: %w", err)
	}

	// Write cache
	entry := CacheEntry{
		CheckedAt:     time.Now(),
		LatestVersion: release.TagName,
	}
	_ = writeCache(entry)

	result := &CheckResult{
		CurrentVersion:  currentVersion,
		LatestVersion:   release.TagName,
		UpdateAvailable: CompareVersions(release.TagName, currentVersion) > 0,
	}

	// Find download URL and checksum URL for this platform
	for _, a := range release.Assets {
		if matchesCurrentPlatformAsset(a.Name) {
			result.DownloadURL = a.BrowserDownloadURL
		}
		if a.Name == checksumsFile {
			result.ChecksumURL = a.BrowserDownloadURL
		}
	}

	return result, nil
}

// ReadCache reads ~/.treehouse/update-check.json and returns a CheckResult
// if the cache exists. Returns nil if missing or corrupt.
func ReadCache(currentVersion string) *CheckResult {
	path := cachePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var entry CacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil
	}

	if entry.LatestVersion == "" {
		return nil
	}

	return &CheckResult{
		CurrentVersion:  currentVersion,
		LatestVersion:   entry.LatestVersion,
		UpdateAvailable: CompareVersions(entry.LatestVersion, currentVersion) > 0,
	}
}

// IsCacheStale returns true if the cache is >24h old, missing, or if the
// cached latest version is not newer than the current version (which means
// the user has updated past the cached latest and we should re-check).
func IsCacheStale(currentVersion string) bool {
	path := cachePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}

	var entry CacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return true
	}

	if time.Since(entry.CheckedAt) > cacheTTL {
		return true
	}

	// If the user has updated past the cached latest version, the cache
	// is effectively stale and we should re-check for even newer versions.
	if currentVersion != "" && CompareVersions(entry.LatestVersion, currentVersion) <= 0 {
		return true
	}

	return false
}

// SpawnBackgroundCheck spawns a detached child process to check for updates.
// The child process inherits TREEHOUSE_NO_UPDATE_CHECK=1 to prevent recursive spawning.
func SpawnBackgroundCheck(currentVersion string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving executable: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("resolving symlinks: %w", err)
	}

	cmd := exec.Command(self, "--update-check", currentVersion)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "TREEHOUSE_NO_UPDATE_CHECK=1")

	devNull, err := os.Open(os.DevNull)
	if err == nil {
		cmd.Stdout = devNull
		cmd.Stderr = devNull
		defer devNull.Close()
	}

	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawning update check: %w", err)
	}

	// Fire and forget — don't wait
	go func() {
		cmd.Wait()
	}()

	return nil
}

// RunBackgroundCheck is the entry point for the --update-check child process.
// It checks for updates, writes the cache, and exits silently.
func RunBackgroundCheck(args []string) {
	if len(args) == 0 {
		os.Exit(1)
	}
	currentVersion := args[0]
	// Best-effort: ignore errors
	CheckLatest(currentVersion)
}

// Apply downloads the archive, extracts the binary, and atomically replaces
// the current executable.
func Apply(result *CheckResult) error {
	if result.DownloadURL == "" {
		return fmt.Errorf("no download URL for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	// Enforce HTTPS on all download URLs to prevent MITM
	if err := requireHTTPS(result.DownloadURL); err != nil {
		return fmt.Errorf("download URL: %w", err)
	}
	if err := requireHTTPS(result.ChecksumURL); err != nil {
		return fmt.Errorf("checksum URL: %w", err)
	}

	// Download to temp file
	archivePath, err := downloadToTemp(result.DownloadURL)
	if err != nil {
		return fmt.Errorf("downloading update: %w", err)
	}
	defer os.Remove(archivePath)

	// Verify checksum
	if err := verifyChecksum(archivePath, result.ChecksumURL); err != nil {
		return fmt.Errorf("checksum verification failed: %w", err)
	}

	// Extract binary
	newBinaryPath, err := extractBinary(archivePath)
	if err != nil {
		return fmt.Errorf("extracting update: %w", err)
	}
	defer os.Remove(newBinaryPath)

	// Remove macOS quarantine
	removeQuarantine(newBinaryPath)

	// Resolve current executable path
	target, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving current executable: %w", err)
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("resolving symlinks: %w", err)
	}

	// Atomically replace
	if err := atomicReplace(target, newBinaryPath); err != nil {
		return fmt.Errorf("replacing binary: %w", err)
	}

	return nil
}

// CompareVersions compares two semver strings.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
// Pre-release versions sort before their release (e.g., v1.0.0-beta < v1.0.0).
func CompareVersions(a, b string) int {
	av := parseVersion(a)
	bv := parseVersion(b)

	for i := 0; i < 3; i++ {
		if av.nums[i] < bv.nums[i] {
			return -1
		}
		if av.nums[i] > bv.nums[i] {
			return 1
		}
	}

	// Same numeric version: pre-release < release
	switch {
	case av.prerelease == "" && bv.prerelease == "":
		return 0
	case av.prerelease != "" && bv.prerelease == "":
		return -1
	case av.prerelease == "" && bv.prerelease != "":
		return 1
	default:
		// Both have pre-release: compare lexically
		if av.prerelease < bv.prerelease {
			return -1
		}
		if av.prerelease > bv.prerelease {
			return 1
		}
		return 0
	}
}

// AssetNameForVersion returns the archive file name for a specific version.
func AssetNameForVersion(version string) string {
	version = strings.TrimPrefix(version, "v")
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("treehouse-v%s-%s-%s%s", version, runtime.GOOS, runtime.GOARCH, ext)
}

type semver struct {
	nums       [3]int
	prerelease string
}

// parseVersion extracts major, minor, patch and pre-release from a version string.
func parseVersion(v string) semver {
	v = strings.TrimPrefix(v, "v")

	var result semver

	// Split off pre-release suffix: "1.2.3-beta.1" → "1.2.3", "beta.1"
	if idx := strings.IndexByte(v, '-'); idx >= 0 {
		result.prerelease = v[idx+1:]
		v = v[:idx]
	}

	parts := strings.SplitN(v, ".", 3)
	for i := 0; i < 3 && i < len(parts); i++ {
		result.nums[i], _ = strconv.Atoi(parts[i])
	}
	return result
}

func requireHTTPS(url string) error {
	if !enforceHTTPS {
		return nil
	}
	if url == "" {
		return fmt.Errorf("URL is empty")
	}
	if !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("refusing non-HTTPS URL: %s", url)
	}
	return nil
}

func cachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, treehouseDir, cacheFileName)
}

func writeCache(entry CacheEntry) error {
	path := cachePath()
	if path == "" {
		return fmt.Errorf("cannot determine home directory")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o644)
}

func verifyChecksum(archivePath, checksumURL string) error {
	if checksumURL == "" {
		return fmt.Errorf("no checksums file in release assets")
	}

	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Get(checksumURL)
	if err != nil {
		return fmt.Errorf("downloading checksums: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("checksums download returned status %d", resp.StatusCode)
	}

	// Parse checksums file (format: "sha256hash  filename")
	var expectedHash string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && matchesCurrentPlatformAsset(fields[1]) {
			expectedHash = fields[0]
			break
		}
	}
	if expectedHash == "" {
		return fmt.Errorf("no checksum found for current platform asset")
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	actualHash := hex.EncodeToString(h.Sum(nil))
	if actualHash != expectedHash {
		return fmt.Errorf("expected %s, got %s", expectedHash, actualHash)
	}

	return nil
}

// matchesCurrentPlatformAsset checks if a filename matches the expected asset
// pattern for the current OS/arch, regardless of version.
func matchesCurrentPlatformAsset(name string) bool {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	suffix := fmt.Sprintf("-%s-%s%s", runtime.GOOS, runtime.GOARCH, ext)
	return strings.HasPrefix(name, "treehouse-v") && strings.HasSuffix(name, suffix)
}

func downloadToTemp(url string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "treehouse-update-*")
	if err != nil {
		return "", err
	}
	defer tmp.Close()

	n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxDownloadSize+1))
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if n > maxDownloadSize {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("download exceeds maximum size of %d bytes", maxDownloadSize)
	}

	return tmp.Name(), nil
}

func extractBinary(archivePath string) (string, error) {
	if runtime.GOOS == "windows" {
		return extractZip(archivePath)
	}
	return extractTarGz(archivePath)
}

func extractTarGz(archivePath string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	binaryName := "treehouse"

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		name := filepath.Base(header.Name)
		if name == binaryName && header.Typeflag == tar.TypeReg {
			tmp, err := os.CreateTemp("", "treehouse-new-*")
			if err != nil {
				return "", err
			}
			n, err := io.Copy(tmp, io.LimitReader(tr, maxBinarySize+1))
			if err != nil {
				tmp.Close()
				os.Remove(tmp.Name())
				return "", err
			}
			if n > maxBinarySize {
				tmp.Close()
				os.Remove(tmp.Name())
				return "", fmt.Errorf("binary exceeds maximum size of %d bytes", maxBinarySize)
			}
			tmp.Close()
			if err := os.Chmod(tmp.Name(), 0o755); err != nil {
				os.Remove(tmp.Name())
				return "", err
			}
			return tmp.Name(), nil
		}
	}

	return "", fmt.Errorf("binary %q not found in archive", binaryName)
}

func extractZip(archivePath string) (string, error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer r.Close()

	binaryName := "treehouse.exe"

	for _, f := range r.File {
		name := filepath.Base(f.Name)
		if name == binaryName {
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			defer rc.Close()

			tmp, err := os.CreateTemp("", "treehouse-new-*.exe")
			if err != nil {
				return "", err
			}
			n, err := io.Copy(tmp, io.LimitReader(rc, maxBinarySize+1))
			if err != nil {
				tmp.Close()
				os.Remove(tmp.Name())
				return "", err
			}
			if n > maxBinarySize {
				tmp.Close()
				os.Remove(tmp.Name())
				return "", fmt.Errorf("binary exceeds maximum size of %d bytes", maxBinarySize)
			}
			tmp.Close()
			if err := os.Chmod(tmp.Name(), 0o755); err != nil {
				os.Remove(tmp.Name())
				return "", err
			}
			return tmp.Name(), nil
		}
	}

	return "", fmt.Errorf("binary %q not found in archive", binaryName)
}

func atomicReplace(target, newBinary string) error {
	// Get permissions from original
	info, err := os.Stat(target)
	if err != nil {
		return err
	}

	// Create temp file in same directory as target (ensures same filesystem for rename)
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".treehouse-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	// Copy new binary to temp location
	src, err := os.Open(newBinary)
	if err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}

	if _, err := io.Copy(tmp, src); err != nil {
		src.Close()
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	src.Close()
	tmp.Close()

	// Set permissions to match original
	if err := os.Chmod(tmpPath, info.Mode()); err != nil {
		os.Remove(tmpPath)
		return err
	}

	// Atomic rename
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return nil
}
