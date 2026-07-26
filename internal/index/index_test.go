package index

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadNonexistent(t *testing.T) {
	idx, err := Load(filepath.Join(t.TempDir(), "no-such-file.yaml"))
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if idx == nil || idx.Files == nil {
		t.Error("expected non-nil empty index")
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".llmdoc", "index.yaml")

	idx := New()
	idx.Files["src/main.go"] = &Entry{
		Summary:     "Entry point.",
		Hash:        "abc123",
		Model:       "test-model",
		GeneratedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Version:     1,
	}

	if err := Save(path, idx); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	e, ok := loaded.Files["src/main.go"]
	if !ok || e == nil {
		t.Fatal("expected entry for src/main.go")
	}
	if e.Summary != "Entry point." {
		t.Errorf("summary mismatch: %q", e.Summary)
	}
	if e.Hash != "abc123" {
		t.Errorf("hash mismatch: %q", e.Hash)
	}
}

func TestSaveAndLoadWithDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".llmdoc", "index.yaml")

	idx := New()
	idx.Files["src/main.go"] = &Entry{
		Summary:     "Entry point.",
		Hash:        "abc123",
		Model:       "test-model",
		GeneratedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Version:     1,
	}
	idx.Directories["src"] = &Entry{
		Summary:     "- Source files",
		Hash:        "def456",
		Model:       "test-model",
		GeneratedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Version:     1,
	}

	if err := Save(path, idx); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	// Check file entry
	e, ok := loaded.Files["src/main.go"]
	if !ok || e == nil {
		t.Fatal("expected entry for src/main.go")
	}
	if e.Summary != "Entry point." {
		t.Errorf("file summary mismatch: %q", e.Summary)
	}

	// Check directory entry
	d, ok := loaded.Directories["src"]
	if !ok || d == nil {
		t.Fatal("expected entry for src directory")
	}
	if d.Summary != "- Source files" {
		t.Errorf("directory summary mismatch: %q", d.Summary)
	}
	if d.Hash != "def456" {
		t.Errorf("directory hash mismatch: %q", d.Hash)
	}
}

func TestBackwardCompat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".llmdoc", "index.yaml")

	// Create an index with only files (no directories section)
	idx := New()
	idx.Files["src/main.go"] = &Entry{
		Summary:     "Entry point.",
		Hash:        "abc123",
		Model:       "test-model",
		GeneratedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Version:     1,
	}

	if err := Save(path, idx); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	// Directories map should be initialized even if not present in YAML
	if loaded.Directories == nil {
		t.Error("expected initialized Directories map")
	}
	if len(loaded.Directories) != 0 {
		t.Errorf("expected empty directories, got %d entries", len(loaded.Directories))
	}
}
