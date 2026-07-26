package annotator

import (
	"testing"

	"github.com/tristanmatthias/llmdoc/internal/config"
	"github.com/tristanmatthias/llmdoc/internal/index"
	"github.com/tristanmatthias/llmdoc/internal/scanner"
)

func TestCollectDirectoriesFiltering(t *testing.T) {
	files := []scanner.FileInfo{
		{RelPath: "src/main.go", Ext: ".go"},
		{RelPath: "src/utils.go", Ext: ".go"},
		{RelPath: "tests/main_test.go", Ext: ".go"},
		{RelPath: "tests/utils_test.go", Ext: ".go"},
		{RelPath: "README.md", Ext: ".md"}, // single file in root-level, should not appear
	}

	fileEntries := map[string]*index.Entry{
		"src/main.go":         {Hash: "hash1", Summary: "Main entry point"},
		"src/utils.go":        {Hash: "hash2", Summary: "Utility functions"},
		"tests/main_test.go":  {Hash: "hash3", Summary: "Main tests"},
		"tests/utils_test.go": {Hash: "hash4", Summary: "Utils tests"},
		"README.md":           {Hash: "hash5", Summary: "README"},
	}

	cfg := &config.Config{}
	cfg.Ignore = []string{} // empty ignore list

	dirs := collectDirectories(files, cfg, fileEntries)

	// Should have 2 directories: src and tests
	if len(dirs) != 2 {
		t.Errorf("expected 2 directories, got %d", len(dirs))
	}

	// Check that src is first (alphabetical order, with trailing slash)
	if dirs[0].RelPath != "src/" {
		t.Errorf("expected first directory to be 'src/', got %q", dirs[0].RelPath)
	}

	// Check file counts
	if len(dirs[0].Files) != 2 {
		t.Errorf("src: expected 2 files, got %d", len(dirs[0].Files))
	}
	if len(dirs[1].Files) != 2 {
		t.Errorf("tests: expected 2 files, got %d", len(dirs[1].Files))
	}
}

func TestCollectDirectoriesSingleFileExcluded(t *testing.T) {
	files := []scanner.FileInfo{
		{RelPath: "src/main.go", Ext: ".go"},
		{RelPath: "src/utils.go", Ext: ".go"},
		{RelPath: "tools/build.go", Ext: ".go"}, // only 1 file, should be excluded
	}

	fileEntries := map[string]*index.Entry{
		"src/main.go":    {Hash: "hash1", Summary: "Main"},
		"src/utils.go":   {Hash: "hash2", Summary: "Utils"},
		"tools/build.go": {Hash: "hash3", Summary: "Build"},
	}

	cfg := &config.Config{}
	cfg.Ignore = []string{}

	dirs := collectDirectories(files, cfg, fileEntries)

	// Should have only 1 directory (src), tools should be excluded
	if len(dirs) != 1 {
		t.Errorf("expected 1 directory, got %d", len(dirs))
	}

	if dirs[0].RelPath != "src/" {
		t.Errorf("expected directory to be 'src/', got %q", dirs[0].RelPath)
	}
}

func TestCollectDirectoriesIgnoredDirectory(t *testing.T) {
	files := []scanner.FileInfo{
		{RelPath: "src/main.go", Ext: ".go"},
		{RelPath: "src/utils.go", Ext: ".go"},
		{RelPath: "vendor/lib.go", Ext: ".go"},
		{RelPath: "vendor/lib2.go", Ext: ".go"},
	}

	fileEntries := map[string]*index.Entry{
		"src/main.go":    {Hash: "hash1", Summary: "Main"},
		"src/utils.go":   {Hash: "hash2", Summary: "Utils"},
		"vendor/lib.go":  {Hash: "hash3", Summary: "Vendor lib"},
		"vendor/lib2.go": {Hash: "hash4", Summary: "Vendor lib 2"},
	}

	cfg := &config.Config{}
	cfg.Ignore = []string{"vendor/"}

	dirs := collectDirectories(files, cfg, fileEntries)

	// Should have only 1 directory (src), vendor should be ignored
	if len(dirs) != 1 {
		t.Errorf("expected 1 directory, got %d", len(dirs))
	}

	if dirs[0].RelPath != "src/" {
		t.Errorf("expected directory to be 'src/', got %q", dirs[0].RelPath)
	}
}

func TestCollectDirectoriesAggregatedHash(t *testing.T) {
	files := []scanner.FileInfo{
		{RelPath: "src/main.go", Ext: ".go"},
		{RelPath: "src/utils.go", Ext: ".go"},
	}

	fileEntries := map[string]*index.Entry{
		"src/main.go":  {Hash: "hash1", Summary: "Main"},
		"src/utils.go": {Hash: "hash2", Summary: "Utils"},
	}

	cfg := &config.Config{}
	cfg.Ignore = []string{}

	dirs := collectDirectories(files, cfg, fileEntries)

	if len(dirs) != 1 {
		t.Fatalf("expected 1 directory")
	}

	// Aggregated hash should be non-empty and consistent
	if dirs[0].AggregatedHash == "" {
		t.Error("expected non-empty aggregated hash")
	}

	// Same files should produce same hash
	dirs2 := collectDirectories(files, cfg, fileEntries)
	if dirs[0].AggregatedHash != dirs2[0].AggregatedHash {
		t.Errorf("aggregated hash mismatch: %q != %q", dirs[0].AggregatedHash, dirs2[0].AggregatedHash)
	}
}

func TestShouldRegenerateDirectoryNew(t *testing.T) {
	idx := &index.Index{
		Files:       make(map[string]*index.Entry),
		Directories: make(map[string]*index.Entry),
	}

	// Directory doesn't exist in index, should regenerate
	should := shouldRegenerateDirectory("src/", "hash1", idx)
	if !should {
		t.Error("expected should regenerate for new directory")
	}
}

func TestShouldRegenerateDirectoryHashChanged(t *testing.T) {
	idx := &index.Index{
		Files: make(map[string]*index.Entry),
		Directories: map[string]*index.Entry{
			"src/": {Hash: "oldhash"},
		},
	}

	// Hash changed, should regenerate
	should := shouldRegenerateDirectory("src/", "newhash", idx)
	if !should {
		t.Error("expected should regenerate when hash changes")
	}
}

func TestShouldRegenerateDirectoryUnchanged(t *testing.T) {
	idx := &index.Index{
		Files: make(map[string]*index.Entry),
		Directories: map[string]*index.Entry{
			"src/": {Hash: "samehash"},
		},
	}

	// Hash unchanged, should not regenerate
	should := shouldRegenerateDirectory("src/", "samehash", idx)
	if should {
		t.Error("expected should NOT regenerate when hash unchanged")
	}
}

func TestShouldRegenerateDirectoryNilIndex(t *testing.T) {
	// Nil index (inline mode), should always regenerate
	should := shouldRegenerateDirectory("src", "hash1", nil)
	if !should {
		t.Error("expected should regenerate with nil index")
	}
}
