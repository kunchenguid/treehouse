
// marker_dangling_test.go — Greptile P1 (PR #134).
//
// NEDEN: WorktreeBackendNameChecked os.Stat kullaniyordu. os.Stat symlink'i
// TAKIP EDER; hedefi silinmis bir .git symlink'inde (dangling) ENOENT doner ve
// os.IsNotExist bunu "marker yok" sayar. Sonuc: HASARLI slot markerless gorunur,
// state.go flavor=="" gorup ATLAR -- slot status'tan SESSIZCE kaybolur.
// Bu test o davranisi kilitler: dangling marker OKUMA HATASI olmali, "yok" degil.

package vcs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestWorktreeBackendNameChecked_DanglingGitSymlinkIsReadFailure — P1 cekirdegi.
// .git bir symlink ve hedefi YOK; os.Stat ENOENT doner ama marker DISKTE.
// Beklenen: hata (okuma basarisizligi), bos ad degil.
func TestWorktreeBackendNameChecked_DanglingGitSymlinkIsReadFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantigi Windows'ta farkli")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, ".git")
	// Hedefi hic olusturmayiz: symlink DANGLING olur.
	if err := os.Symlink(filepath.Join(dir, "nonexistent-target"), link); err != nil {
		t.Fatalf("symlink kurulamadi: %v", err)
	}

	name, err := WorktreeBackendNameChecked(dir)
	if err == nil {
		t.Fatalf("dangling .git symlink OKUMA HATASI olmaliydi; name=%q err=nil (markerless sayildi)", name)
	}
	if name != "" {
		t.Fatalf("hata halinde ad bos olmaliydi, %q geldi", name)
	}
}

// TestWorktreeBackendNameChecked_AbsentMarkerIsNotAnError — yanlis-pozitif freni.
// Marker GERCEKTEN yoksa hata DEGIL, bos ad + nil donmeli (mevcut davranis korunur).
func TestWorktreeBackendNameChecked_AbsentMarkerIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	name, err := WorktreeBackendNameChecked(dir)
	if err != nil {
		t.Fatalf("markersiz dizin hata vermemeli: %v", err)
	}
	if name != "" {
		t.Fatalf("markersiz dizin bos ad donmeli, %q geldi", name)
	}
}

// TestWorktreeBackendNameChecked_LiveSymlinkStillResolves — mutlu yol korunur.
// Hedefi VAR olan .git symlink'i eskisi gibi "git" dondurmeli.
func TestWorktreeBackendNameChecked_LiveSymlinkStillResolves(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantigi Windows'ta farkli")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real-git-dir")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("hedef dizin kurulamadi: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("symlink kurulamadi: %v", err)
	}
	name, err := WorktreeBackendNameChecked(dir)
	if err != nil {
		t.Fatalf("canli symlink hata vermemeli: %v", err)
	}
	if name != "git" {
		t.Fatalf("canli symlink \"git\" donmeli, %q geldi", name)
	}
}
