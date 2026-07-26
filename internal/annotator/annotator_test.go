package annotator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tristanmatthias/llmdoc/internal/comment"
	"github.com/tristanmatthias/llmdoc/internal/config"
	"github.com/tristanmatthias/llmdoc/internal/hasher"
	"github.com/tristanmatthias/llmdoc/internal/index"
	"github.com/tristanmatthias/llmdoc/internal/llm"
	"github.com/tristanmatthias/llmdoc/internal/scanner"
)

// mockProvider returns a fixed summary for any request.
type mockProvider struct {
	summary string
}

func (m *mockProvider) Summarize(_ context.Context, req llm.SummaryRequest) (string, llm.TokenUsage, error) {
	if m.summary != "" {
		return m.summary, llm.TokenUsage{InputTokens: 100, OutputTokens: 20}, nil
	}
	return "Mock summary for " + req.FilePath + ".", llm.TokenUsage{InputTokens: 100, OutputTokens: 20}, nil
}

// collect drains a result channel into a slice.
func collect(ch <-chan Result) []Result {
	var out []Result
	for r := range ch {
		out = append(out, r)
	}
	return out
}

func TestAnnotate_CreatesBlockOnNewFile(t *testing.T) {
	dir := t.TempDir()

	goFile := filepath.Join(dir, "main.go")
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Extensions:  []string{".go"},
		Ignore:      []string{},
		Concurrency: 1,
		Model:       "test-model",
	}

	provider := &mockProvider{summary: "Entry point of the test binary."}
	_, ch, err := Run(context.Background(), dir, cfg, provider, Options{})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	results := collect(ch)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != StatusCreated {
		t.Errorf("expected StatusCreated, got %v", results[0].Status)
	}

	content, err := os.ReadFile(goFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "llmdoc:start") {
		t.Error("block not present in annotated file")
	}
	if !strings.Contains(string(content), "Entry point of the test binary.") {
		t.Error("summary not in annotated file")
	}
	if !strings.Contains(string(content), "package main") {
		t.Error("original content was lost")
	}
}

func TestAnnotate_UnchangedSkipsLLM(t *testing.T) {
	dir := t.TempDir()

	pyFile := filepath.Join(dir, "app.py")
	original := "def main():\n    pass\n"
	if err := os.WriteFile(pyFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Extensions:  []string{".py"},
		Ignore:      []string{},
		Concurrency: 1,
		Model:       "test-model",
	}

	callCount := 0
	provider := &countingProvider{t: t, callCount: &callCount}

	// First run — should annotate
	_, ch, err := Run(context.Background(), dir, cfg, provider, Options{})
	if err != nil {
		t.Fatalf("first Run error: %v", err)
	}
	results := collect(ch)
	if results[0].Status != StatusCreated {
		t.Errorf("expected StatusCreated, got %v", results[0].Status)
	}
	if callCount != 1 {
		t.Errorf("expected 1 LLM call, got %d", callCount)
	}

	// Second run on unchanged file — should skip
	_, ch, err = Run(context.Background(), dir, cfg, provider, Options{})
	if err != nil {
		t.Fatalf("second Run error: %v", err)
	}
	results = collect(ch)
	if results[0].Status != StatusUnchanged {
		t.Errorf("expected StatusUnchanged on second run, got %v", results[0].Status)
	}
	if callCount != 1 {
		t.Errorf("expected still 1 LLM call total, got %d", callCount)
	}
}

func TestAnnotate_UpdatesWhenFileChanges(t *testing.T) {
	dir := t.TempDir()

	tsFile := filepath.Join(dir, "utils.ts")
	original := "export function add(a: number, b: number): number { return a + b; }\n"
	if err := os.WriteFile(tsFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Extensions:  []string{".ts"},
		Ignore:      []string{},
		Concurrency: 1,
		Model:       "test-model",
	}

	provider := &mockProvider{summary: "Utility functions."}

	// First run — creates the block
	_, ch, _ := Run(context.Background(), dir, cfg, provider, Options{})
	collect(ch)

	// Simulate editing the file body: append to the annotated file (block is preserved)
	f, err := os.OpenFile(tsFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\nexport function sub(a: number, b: number): number { return a - b; }\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}

	// Second run — should detect change
	_, ch, err = Run(context.Background(), dir, cfg, provider, Options{})
	if err != nil {
		t.Fatal(err)
	}
	results := collect(ch)
	if results[0].Status != StatusUpdated {
		t.Errorf("expected StatusUpdated after file change, got %v", results[0].Status)
	}
}

func TestAnnotate_HashStable(t *testing.T) {
	dir := t.TempDir()

	goFile := filepath.Join(dir, "svc.go")
	original := "package svc\n\ntype Service struct{}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Extensions:  []string{".go"},
		Ignore:      []string{},
		Concurrency: 1,
		Model:       "test-model",
	}

	provider := &mockProvider{}
	_, ch, _ := Run(context.Background(), dir, cfg, provider, Options{})
	collect(ch)

	content, _ := os.ReadFile(goFile)
	syntax, _ := comment.ForExtension(".go")
	block, err := comment.Parse(string(content), syntax)
	if err != nil || block == nil {
		t.Fatalf("expected block in annotated file, got block=%v err=%v", block, err)
	}

	currentHash := hasher.ComputeHash(content)
	if currentHash != block.ContentHash {
		t.Errorf("hash mismatch after annotation!\n  stored:  %s\n  current: %s", block.ContentHash, currentHash)
	}
}

func TestAnnotate_DryRun(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	original := "package main\nfunc main() {}\n"
	os.WriteFile(goFile, []byte(original), 0644)

	cfg := &config.Config{
		Extensions:  []string{".go"},
		Ignore:      []string{},
		Concurrency: 1,
	}
	provider := &mockProvider{}

	_, ch, _ := Run(context.Background(), dir, cfg, provider, Options{DryRun: true})
	results := collect(ch)
	if results[0].Status != StatusCreated {
		t.Errorf("dry run should still report would-create, got %v", results[0].Status)
	}

	content, _ := os.ReadFile(goFile)
	if strings.Contains(string(content), "llmdoc:start") {
		t.Error("dry run should not modify files")
	}
}

// TestAnnotate_DryRunWithDirectories verifies that dry-run mode generates directory results
// with estimated tokens when directories are enabled, enabling accurate cost projection.
func TestAnnotate_DryRunWithDirectories(t *testing.T) {
	dir := t.TempDir()

	// Create a subdirectory with 2 files to trigger directory processing
	subdir := filepath.Join(dir, "src")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(subdir, "main.go"), []byte("package main\nfunc main() {}"), 0644)
	os.WriteFile(filepath.Join(subdir, "utils.go"), []byte("package main\nfunc help() {}"), 0644)

	cfg := &config.Config{
		Extensions:                 []string{".go"},
		Ignore:                     []string{},
		Concurrency:                1,
		Model:                      "test-model",
		Mode:                       "index",
		IndexFile:                  filepath.Join(dir, ".llmdoc", "index.yaml"),
		GenerateDirectorySummaries: true,
	}

	provider := &mockProvider{}

	// Run in dry-run mode with directories enabled
	_, ch, err := Run(context.Background(), dir, cfg, provider, Options{DryRun: true})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	results := collect(ch)

	// Separate file and directory results
	var fileResults, dirResults []Result
	for _, r := range results {
		if r.DirectoryPath != "" {
			dirResults = append(dirResults, r)
		} else if r.File.RelPath != "" {
			fileResults = append(fileResults, r)
		}
	}

	// Should have 2 file results and 1 directory result
	if len(fileResults) != 2 {
		t.Errorf("expected 2 file results, got %d", len(fileResults))
	}
	if len(dirResults) != 1 {
		t.Errorf("expected 1 directory result, got %d", len(dirResults))
	}

	// File results should have EstimatedTokens (dry-run mode)
	for _, r := range fileResults {
		if r.EstimatedTokens <= 0 {
			t.Errorf("expected file EstimatedTokens > 0, got %d", r.EstimatedTokens)
		}
		if r.TokensUsed.InputTokens != 0 || r.TokensUsed.OutputTokens != 0 {
			t.Error("expected zero TokensUsed in dry-run (estimated instead)")
		}
	}

	// Directory result should have EstimatedTokens
	if len(dirResults) > 0 {
		if dirResults[0].EstimatedTokens <= 0 {
			t.Errorf("expected directory EstimatedTokens > 0, got %d", dirResults[0].EstimatedTokens)
		}
		if dirResults[0].TokensUsed.InputTokens != 0 || dirResults[0].TokensUsed.OutputTokens != 0 {
			t.Error("expected zero TokensUsed in dry-run (estimated instead)")
		}
	}

	// Index file should not be written in dry-run
	if _, err := os.Stat(cfg.IndexFile); err == nil {
		t.Error("index file should not be written in dry-run mode")
	}
}

func TestAnnotate_TokensReported(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "svc.go")
	os.WriteFile(goFile, []byte("package svc\n"), 0644)

	cfg := &config.Config{
		Extensions:  []string{".go"},
		Ignore:      []string{},
		Concurrency: 1,
		Model:       "test-model",
	}
	provider := &mockProvider{}

	_, ch, _ := Run(context.Background(), dir, cfg, provider, Options{})
	results := collect(ch)

	if results[0].TokensUsed.InputTokens != 100 {
		t.Errorf("expected 100 input tokens, got %d", results[0].TokensUsed.InputTokens)
	}
	if results[0].TokensUsed.OutputTokens != 20 {
		t.Errorf("expected 20 output tokens, got %d", results[0].TokensUsed.OutputTokens)
	}
}

func TestAnnotate_IndexMode(t *testing.T) {
	dir := t.TempDir()

	goFile := filepath.Join(dir, "svc.go")
	original := "package svc\n\ntype Service struct{}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Extensions:  []string{".go"},
		Ignore:      []string{},
		Concurrency: 1,
		Model:       "test-model",
		Mode:        "index",
		IndexFile:   filepath.Join(dir, ".llmdoc", "index.yaml"),
	}

	provider := &mockProvider{summary: "Service layer."}
	_, ch, err := Run(context.Background(), dir, cfg, provider, Options{})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	results := collect(ch)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != StatusCreated {
		t.Errorf("expected StatusCreated, got %v", results[0].Status)
	}

	// Source file must NOT be modified
	content, _ := os.ReadFile(goFile)
	if strings.Contains(string(content), "llmdoc:start") {
		t.Error("index mode must not modify source files")
	}
	if string(content) != original {
		t.Errorf("source file was modified: %q", content)
	}

	// Index file must exist and contain the entry
	idxPath := filepath.Join(dir, ".llmdoc", "index.yaml")
	if _, err := os.Stat(idxPath); err != nil {
		t.Fatalf("index file not created: %v", err)
	}
}

func TestAnnotate_IndexMode_UnchangedSkipsLLM(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	os.WriteFile(goFile, []byte("package main\nfunc main() {}\n"), 0644)

	cfg := &config.Config{
		Extensions:  []string{".go"},
		Ignore:      []string{},
		Concurrency: 1,
		Model:       "test-model",
		Mode:        "index",
		IndexFile:   filepath.Join(dir, ".llmdoc", "index.yaml"),
	}

	calls := 0
	provider := &countingProvider{t: t, callCount: &calls}

	_, ch, _ := Run(context.Background(), dir, cfg, provider, Options{})
	results := collect(ch)
	if results[0].Status != StatusCreated {
		t.Errorf("first run: expected StatusCreated, got %v", results[0].Status)
	}
	if calls != 1 {
		t.Errorf("expected 1 LLM call, got %d", calls)
	}

	// Second run — hash unchanged, should skip
	_, ch, _ = Run(context.Background(), dir, cfg, provider, Options{})
	results = collect(ch)
	if results[0].Status != StatusUnchanged {
		t.Errorf("second run: expected StatusUnchanged, got %v", results[0].Status)
	}
	if calls != 1 {
		t.Errorf("expected no additional LLM calls, got %d total", calls)
	}
}

// TestAnnotate_MigrateInlineToIndex verifies that switching from inline to index mode
// reuses the inline block's summary — no LLM call is made.
func TestAnnotate_MigrateInlineToIndex(t *testing.T) {
	dir := t.TempDir()

	goFile := filepath.Join(dir, "svc.go")
	original := "package svc\n\ntype Service struct{}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	indexFile := filepath.Join(dir, ".llmdoc", "index.yaml")
	calls := 0
	provider := &countingProvider{t: t, callCount: &calls}

	// First run in inline mode — annotates the source file with a block comment.
	inlineCfg := &config.Config{
		Extensions:  []string{".go"},
		Ignore:      []string{},
		Concurrency: 1,
		Model:       "test-model",
		Mode:        "inline",
	}
	_, ch, err := Run(context.Background(), dir, inlineCfg, provider, Options{})
	if err != nil {
		t.Fatalf("inline Run error: %v", err)
	}
	collect(ch)
	if calls != 1 {
		t.Fatalf("expected 1 LLM call after inline run, got %d", calls)
	}

	contentAfterInline, _ := os.ReadFile(goFile)
	if !strings.Contains(string(contentAfterInline), "llmdoc:start") {
		t.Fatal("expected inline block in file after inline run")
	}

	// Switch to index mode — should reuse the inline block summary (StatusCleaned, zero LLM calls).
	indexCfg := &config.Config{
		Extensions:  []string{".go"},
		Ignore:      []string{},
		Concurrency: 1,
		Model:       "test-model",
		Mode:        "index",
		IndexFile:   indexFile,
	}
	_, ch, err = Run(context.Background(), dir, indexCfg, provider, Options{})
	if err != nil {
		t.Fatalf("index Run error: %v", err)
	}
	results := collect(ch)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != StatusCleaned {
		t.Errorf("expected StatusCleaned (zero-cost migration), got %v", results[0].Status)
	}
	if calls != 1 {
		t.Errorf("expected still 1 LLM call total (zero cost), got %d", calls)
	}

	// Source file must have the inline block stripped.
	contentAfterIndex, _ := os.ReadFile(goFile)
	if strings.Contains(string(contentAfterIndex), "llmdoc:start") {
		t.Error("inline block should have been stripped from source file")
	}
	if !strings.Contains(string(contentAfterIndex), "package svc") {
		t.Error("source content was lost after stripping")
	}

	// Subsequent index-mode run must be fully unchanged.
	_, ch, _ = Run(context.Background(), dir, indexCfg, provider, Options{})
	results = collect(ch)
	if results[0].Status != StatusUnchanged {
		t.Errorf("subsequent index run: expected StatusUnchanged, got %v", results[0].Status)
	}
	if calls != 1 {
		t.Errorf("expected still 1 LLM call total, got %d", calls)
	}
}

// TestAnnotate_MigrateIndexToInline verifies that switching from index to inline mode
// reuses the index summary — no LLM call is made.
func TestAnnotate_MigrateIndexToInline(t *testing.T) {
	dir := t.TempDir()

	goFile := filepath.Join(dir, "svc.go")
	original := "package svc\n\ntype Service struct{}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	indexFile := filepath.Join(dir, ".llmdoc", "index.yaml")
	calls := 0
	provider := &countingProvider{t: t, callCount: &calls}

	// First run in index mode.
	indexCfg := &config.Config{
		Extensions:  []string{".go"},
		Ignore:      []string{},
		Concurrency: 1,
		Model:       "test-model",
		Mode:        "index",
		IndexFile:   indexFile,
	}
	_, ch, err := Run(context.Background(), dir, indexCfg, provider, Options{})
	if err != nil {
		t.Fatalf("index Run error: %v", err)
	}
	collect(ch)
	if calls != 1 {
		t.Fatalf("expected 1 LLM call after index run, got %d", calls)
	}

	// Switch to inline mode — should reuse the index summary (StatusMigrated, zero LLM calls).
	inlineCfg := &config.Config{
		Extensions:  []string{".go"},
		Ignore:      []string{},
		Concurrency: 1,
		Model:       "test-model",
		Mode:        "inline",
		IndexFile:   indexFile, // needed so migrationIdx can load it
	}
	_, ch, err = Run(context.Background(), dir, inlineCfg, provider, Options{})
	if err != nil {
		t.Fatalf("inline Run error: %v", err)
	}
	results := collect(ch)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != StatusMigrated {
		t.Errorf("expected StatusMigrated (zero-cost migration), got %v", results[0].Status)
	}
	if calls != 1 {
		t.Errorf("expected still 1 LLM call total (zero cost), got %d", calls)
	}

	// Source file must now have the inline block.
	contentAfterInline, _ := os.ReadFile(goFile)
	if !strings.Contains(string(contentAfterInline), "llmdoc:start") {
		t.Error("inline block should have been written to source file")
	}
	if !strings.Contains(string(contentAfterInline), "package svc") {
		t.Error("source content was lost after migration")
	}

	// Subsequent inline-mode run must be fully unchanged.
	_, ch, _ = Run(context.Background(), dir, inlineCfg, provider, Options{})
	results = collect(ch)
	if results[0].Status != StatusUnchanged {
		t.Errorf("subsequent inline run: expected StatusUnchanged, got %v", results[0].Status)
	}
	if calls != 1 {
		t.Errorf("expected still 1 LLM call total, got %d", calls)
	}
}

// TestAnnotate_StatusCleaned verifies that a file with an up-to-date index entry
// but a leftover inline block gets StatusCleaned (block stripped, no LLM call).
func TestAnnotate_StatusCleaned(t *testing.T) {
	dir := t.TempDir()

	goFile := filepath.Join(dir, "svc.go")
	original := "package svc\n\ntype Service struct{}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	indexFile := filepath.Join(dir, ".llmdoc", "index.yaml")
	indexCfg := &config.Config{
		Extensions:  []string{".go"},
		Ignore:      []string{},
		Concurrency: 1,
		Model:       "test-model",
		Mode:        "index",
		IndexFile:   indexFile,
	}

	calls := 0
	provider := &countingProvider{t: t, callCount: &calls}

	// First index run — creates the index entry.
	_, ch, _ := Run(context.Background(), dir, indexCfg, provider, Options{})
	collect(ch)
	if calls != 1 {
		t.Fatalf("expected 1 LLM call, got %d", calls)
	}

	// Manually graft an inline block onto the source file to simulate a stale artifact.
	fileContent, _ := os.ReadFile(goFile)
	fakeBlock := "/*llmdoc:start\nsummary: Old inline summary.\nhash: deadbeef\nmodel: test\ngenerated: 2026-01-01T00:00:00Z\nversion: 1\nllmdoc:end*/\n"
	os.WriteFile(goFile, append([]byte(fakeBlock), fileContent...), 0644)

	// Second index run — index hash matches stripped content; inline block → StatusCleaned.
	_, ch, _ = Run(context.Background(), dir, indexCfg, provider, Options{})
	results := collect(ch)

	if calls != 1 {
		t.Errorf("expected no additional LLM calls, got %d total", calls)
	}
	if len(results) != 1 || results[0].Status != StatusCleaned {
		t.Errorf("expected StatusCleaned, got %v", results[0].Status)
	}

	finalContent, _ := os.ReadFile(goFile)
	if strings.Contains(string(finalContent), "llmdoc:start") {
		t.Error("inline block should have been stripped")
	}
}

// countingProvider counts Summarize calls.
type countingProvider struct {
	t         *testing.T
	callCount *int
}

func (c *countingProvider) Summarize(_ context.Context, req llm.SummaryRequest) (string, llm.TokenUsage, error) {
	*c.callCount++
	return "Summary for " + req.FilePath, llm.TokenUsage{}, nil
}

// panicProvider panics if Summarize is called, used to verify dry-run doesn't call LLM.
type panicProvider struct{}

func (p *panicProvider) Summarize(_ context.Context, req llm.SummaryRequest) (string, llm.TokenUsage, error) {
	panic("Summarize should not be called in dry-run mode")
}

// TestProcessDirectoryDryRun verifies that processDirectory correctly handles dry-run mode.
func TestProcessDirectoryDryRun(t *testing.T) {
	tests := []struct {
		name              string
		fileSummaries     []string
		expectedMinTokens int // minimum expected estimated tokens
		shouldNotCallLLM  bool
	}{
		{
			name:              "dry-run with file summaries",
			fileSummaries:     []string{"File handler utilities.", "HTTP request router.", "JSON encoder for API responses."},
			expectedMinTokens: 200, // at least the overhead
			shouldNotCallLLM:  true,
		},
		{
			name:              "dry-run with empty file summaries",
			fileSummaries:     []string{},
			expectedMinTokens: 200, // just the overhead (0 chars / 4 + 200)
			shouldNotCallLLM:  true,
		},
		{
			name:              "dry-run with single long summary",
			fileSummaries:     []string{"This is a very long file summary that contains detailed information about what the file does. It includes multiple responsibilities and features that the file implements for the application."},
			expectedMinTokens: 200, // overhead + char-based estimate
			shouldNotCallLLM:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock index with file entries
			idx := &index.Index{
				Files:       make(map[string]*index.Entry),
				Directories: make(map[string]*index.Entry),
			}

			// Populate index with file summaries
			fileInfo := []scanner.FileInfo{}
			for i, summary := range tt.fileSummaries {
				relPath := filepath.Join("src", fmt.Sprintf("file%d.go", i))
				fileInfo = append(fileInfo, scanner.FileInfo{RelPath: relPath})
				idx.Files[relPath] = &index.Entry{
					Summary: summary,
					Hash:    fmt.Sprintf("hash%d", i),
					Model:   "test-model",
				}
			}

			// Create a DirectoryToProcess
			dir := DirectoryToProcess{
				RelPath:        "src",
				Files:          fileInfo,
				AggregatedHash: "aggregated-hash-123",
			}

			cfg := &config.Config{
				Model: "test-model",
			}

			// Use panicProvider if we should verify LLM is not called
			var provider llm.Provider
			if tt.shouldNotCallLLM {
				provider = &panicProvider{}
			} else {
				provider = &mockProvider{}
			}

			// Call processDirectory with DryRun: true
			var idxMu sync.Mutex
			result := processDirectory(context.Background(), dir, cfg, provider, Options{DryRun: true}, idx, &idxMu)

			// Verify result properties for dry-run mode
			if result.DirectoryPath != "src" {
				t.Errorf("expected DirectoryPath 'src', got %q", result.DirectoryPath)
			}
			if result.Status != StatusCreated {
				t.Errorf("expected Status StatusCreated, got %v", result.Status)
			}
			if result.EstimatedTokens <= 0 {
				t.Errorf("expected EstimatedTokens > 0, got %d", result.EstimatedTokens)
			}
			if result.EstimatedTokens < tt.expectedMinTokens {
				t.Errorf("expected EstimatedTokens >= %d, got %d", tt.expectedMinTokens, result.EstimatedTokens)
			}

			// In dry-run, TokensUsed should be zero (no LLM call)
			if result.TokensUsed.InputTokens != 0 || result.TokensUsed.OutputTokens != 0 {
				t.Errorf("expected zero TokensUsed in dry-run, got InputTokens=%d OutputTokens=%d",
					result.TokensUsed.InputTokens, result.TokensUsed.OutputTokens)
			}

			// In dry-run, Err should be nil
			if result.Err != nil {
				t.Errorf("expected no error in dry-run, got %v", result.Err)
			}

			// Most importantly: the directory should NOT be added to the index in dry-run mode
			if len(idx.Directories) > 0 {
				t.Errorf("expected directory to NOT be added to index in dry-run, but idx.Directories has %d entries", len(idx.Directories))
			}
		})
	}
}

// TestProcessDirectoryDryRunTokenEstimation verifies the token estimation formula.
func TestProcessDirectoryDryRunTokenEstimation(t *testing.T) {
	// Test the formula: estimatedInputTokens := (totalChars / 4) + 200
	tests := []struct {
		name           string
		fileSummaries  []string
		expectedTokens int // exact expected token count
	}{
		{
			name:          "empty summaries",
			fileSummaries: []string{},
			// (0 / 4) + 200 + SummaryOutputTokens
			expectedTokens: 200, // assuming SummaryOutputTokens is also tested separately
		},
		{
			name:          "one small summary (20 chars)",
			fileSummaries: []string{"Utility helpers.   "},
			// (20 / 4) + 200 + SummaryOutputTokens = 5 + 200 + output
			expectedTokens: 0, // we'll calculate exact value based on actual output tokens
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := &index.Index{
				Files:       make(map[string]*index.Entry),
				Directories: make(map[string]*index.Entry),
			}

			fileInfo := []scanner.FileInfo{}
			totalChars := 0
			for i, summary := range tt.fileSummaries {
				relPath := filepath.Join("src", fmt.Sprintf("f%d.go", i))
				fileInfo = append(fileInfo, scanner.FileInfo{RelPath: relPath})
				idx.Files[relPath] = &index.Entry{Summary: summary}
				totalChars += len(summary)
			}

			dir := DirectoryToProcess{
				RelPath:        "src",
				Files:          fileInfo,
				AggregatedHash: "hash",
			}

			cfg := &config.Config{Model: "test-model"}

			var idxMu sync.Mutex
			result := processDirectory(context.Background(), dir, cfg, &panicProvider{}, Options{DryRun: true}, idx, &idxMu)

			// Verify formula: (totalChars / 4) + 200 + SummaryOutputTokens
			expectedEstimated := (totalChars / 4) + 200
			minExpected := expectedEstimated // without SummaryOutputTokens for this check
			if result.EstimatedTokens < minExpected {
				t.Errorf("estimated tokens %d is less than minimum %d (based on %d chars)",
					result.EstimatedTokens, minExpected, totalChars)
			}

			// Should have output tokens added
			if result.EstimatedTokens <= expectedEstimated {
				t.Errorf("estimated tokens %d should include SummaryOutputTokens (at least %d)",
					result.EstimatedTokens, expectedEstimated)
			}
		})
	}
}

// TestGenerateDirectorySummariesFlag tests the behavior of the generate_directory_summaries config flag
// across all combinations of mode and flag value.
func TestGenerateDirectorySummariesFlag(t *testing.T) {
	tests := []struct {
		name                           string
		mode                           string
		generateDirectorySummariesFlag bool
		expectedFileResults            int
		expectedDirResults             int
		expectedTotalLLMCalls          int // files only, or files + directories
	}{
		{
			name:                           "index mode with flag enabled (default)",
			mode:                           "index",
			generateDirectorySummariesFlag: true,
			expectedFileResults:            2, // 2 files in the directory
			expectedDirResults:             1, // 1 directory summary
			expectedTotalLLMCalls:          3, // 2 files + 1 directory
		},
		{
			name:                           "index mode with flag disabled",
			mode:                           "index",
			generateDirectorySummariesFlag: false,
			expectedFileResults:            2, // 2 files in the directory
			expectedDirResults:             0, // no directory summary
			expectedTotalLLMCalls:          2, // 2 files only
		},
		{
			name:                           "inline mode with flag enabled",
			mode:                           "inline",
			generateDirectorySummariesFlag: true,
			expectedFileResults:            2, // 2 files in the directory
			expectedDirResults:             0, // flag ignored in inline mode
			expectedTotalLLMCalls:          2, // 2 files only (inline doesn't generate directories)
		},
		{
			name:                           "inline mode with flag disabled",
			mode:                           "inline",
			generateDirectorySummariesFlag: false,
			expectedFileResults:            2, // 2 files in the directory
			expectedDirResults:             0, // flag ignored in inline mode
			expectedTotalLLMCalls:          2, // 2 files only (inline doesn't generate directories)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			// Create a subdirectory with 2 files (to trigger directory processing when enabled)
			subdir := filepath.Join(dir, "src")
			if err := os.MkdirAll(subdir, 0755); err != nil {
				t.Fatal(err)
			}

			file1 := filepath.Join(subdir, "main.go")
			file2 := filepath.Join(subdir, "utils.go")
			os.WriteFile(file1, []byte("package main\nfunc main() {}"), 0644)
			os.WriteFile(file2, []byte("package main\nfunc helper() {}"), 0644)

			// Set up config based on test parameters
			indexFile := ""
			if tt.mode == "index" {
				indexFile = filepath.Join(dir, ".llmdoc", "index.yaml")
				os.MkdirAll(filepath.Dir(indexFile), 0755)
			}

			cfg := &config.Config{
				Extensions:                 []string{".go"},
				Ignore:                     []string{},
				Concurrency:                1,
				Model:                      "test-model",
				Mode:                       tt.mode,
				IndexFile:                  indexFile,
				GenerateDirectorySummaries: tt.generateDirectorySummariesFlag,
			}

			callCount := 0
			provider := &countingProvider{t: t, callCount: &callCount}

			_, ch, err := Run(context.Background(), dir, cfg, provider, Options{})
			if err != nil {
				t.Fatalf("Run error: %v", err)
			}

			results := collect(ch)

			// Count file and directory results
			fileResults := 0
			dirResults := 0
			for _, r := range results {
				if r.DirectoryPath != "" {
					dirResults++
				} else if r.File.RelPath != "" {
					fileResults++
				}
			}

			if fileResults != tt.expectedFileResults {
				t.Errorf("expected %d file results, got %d", tt.expectedFileResults, fileResults)
			}
			if dirResults != tt.expectedDirResults {
				t.Errorf("expected %d directory results, got %d", tt.expectedDirResults, dirResults)
			}
			if callCount != tt.expectedTotalLLMCalls {
				t.Errorf("expected %d LLM calls, got %d", tt.expectedTotalLLMCalls, callCount)
			}
		})
	}
}

// TestDryRunTokenEstimationWithDirectorySummaries tests token estimation in dry-run mode.
// Directories cannot be processed in dry-run mode because file hashes aren't stored, but we
// verify that when directories ARE processed (in normal mode), tokens are correctly estimated.
func TestDryRunTokenEstimationWithDirectorySummaries(t *testing.T) {
	tests := []struct {
		name                           string
		mode                           string
		generateDirectorySummariesFlag bool
		dryRun                         bool
		expectedFileResults            int
		expectedDirResults             int
		hasFileTokens                  bool
		hasDirTokens                   bool
	}{
		{
			name:                           "dry-run index mode with flag enabled - files and dirs with estimates",
			mode:                           "index",
			generateDirectorySummariesFlag: true,
			dryRun:                         true,
			expectedFileResults:            2,
			expectedDirResults:             1, // dry-run now generates dirs with estimated tokens
			hasFileTokens:                  true,
			hasDirTokens:                   true,
		},
		{
			name:                           "dry-run index mode with flag disabled - files only",
			mode:                           "index",
			generateDirectorySummariesFlag: false,
			dryRun:                         true,
			expectedFileResults:            2,
			expectedDirResults:             0,
			hasFileTokens:                  true,
			hasDirTokens:                   false,
		},
		{
			name:                           "normal mode index with flag enabled - includes dir tokens",
			mode:                           "index",
			generateDirectorySummariesFlag: true,
			dryRun:                         false,
			expectedFileResults:            2,
			expectedDirResults:             1,
			hasFileTokens:                  true,
			hasDirTokens:                   true,
		},
		{
			name:                           "normal mode index with flag disabled - only file tokens",
			mode:                           "index",
			generateDirectorySummariesFlag: false,
			dryRun:                         false,
			expectedFileResults:            2,
			expectedDirResults:             0,
			hasFileTokens:                  true,
			hasDirTokens:                   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			// Create a subdirectory with 2 files
			subdir := filepath.Join(dir, "src")
			os.MkdirAll(subdir, 0755)
			os.WriteFile(filepath.Join(subdir, "main.go"), []byte("package main\nfunc main() {}"), 0644)
			os.WriteFile(filepath.Join(subdir, "utils.go"), []byte("package main\nfunc helper() {}"), 0644)

			indexFile := ""
			if tt.mode == "index" {
				indexFile = filepath.Join(dir, ".llmdoc", "index.yaml")
				os.MkdirAll(filepath.Dir(indexFile), 0755)
			}

			cfg := &config.Config{
				Extensions:                 []string{".go"},
				Ignore:                     []string{},
				Concurrency:                1,
				Model:                      "test-model",
				Mode:                       tt.mode,
				IndexFile:                  indexFile,
				GenerateDirectorySummaries: tt.generateDirectorySummariesFlag,
			}

			provider := &mockProvider{}

			_, ch, err := Run(context.Background(), dir, cfg, provider, Options{DryRun: tt.dryRun})
			if err != nil {
				t.Fatalf("Run error: %v", err)
			}

			results := collect(ch)

			// Separate file and directory results
			var fileResults, dirResults []Result
			for _, r := range results {
				if r.DirectoryPath != "" {
					dirResults = append(dirResults, r)
				} else if r.File.RelPath != "" {
					fileResults = append(fileResults, r)
				}
			}

			if len(fileResults) != tt.expectedFileResults {
				t.Errorf("expected %d file results, got %d", tt.expectedFileResults, len(fileResults))
			}
			if len(dirResults) != tt.expectedDirResults {
				t.Errorf("expected %d directory results, got %d", tt.expectedDirResults, len(dirResults))
			}

			// Verify file results have EstimatedTokens (in dry-run) or TokensUsed (in normal mode)
			for _, r := range fileResults {
				if tt.dryRun && r.EstimatedTokens <= 0 {
					t.Errorf("expected file result to have estimated tokens > 0 in dry-run, got %d", r.EstimatedTokens)
				} else if !tt.dryRun && r.TokensUsed.OutputTokens == 0 {
					// In normal mode, should have actual tokens from LLM
					t.Errorf("expected file result to have actual tokens in normal mode")
				}
			}

			// Verify directory results have EstimatedTokens/TokensUsed when expected
			if tt.hasDirTokens {
				if len(dirResults) != tt.expectedDirResults {
					t.Fatalf("expected %d directory results, got %d", tt.expectedDirResults, len(dirResults))
				}
				for _, r := range dirResults {
					if tt.dryRun && r.EstimatedTokens <= 0 {
						t.Errorf("expected directory result to have estimated tokens > 0, got %d", r.EstimatedTokens)
					} else if !tt.dryRun && r.TokensUsed.OutputTokens == 0 {
						t.Errorf("expected directory result to have actual tokens in normal mode")
					}
				}
			}

			// Verify no directory results when not expected
			if !tt.hasDirTokens && len(dirResults) > 0 {
				t.Errorf("expected no directory results, got %d", len(dirResults))
			}
		})
	}
}

// TestConcurrentFileProcessing tests that multiple files are processed concurrently
// with high concurrency settings without data races or deadlocks.
func TestConcurrentFileProcessing(t *testing.T) {
	dir := t.TempDir()

	// Create 20 files to exercise concurrency
	const numFiles = 20
	for i := 0; i < numFiles; i++ {
		goFile := filepath.Join(dir, fmt.Sprintf("file%d.go", i))
		content := fmt.Sprintf("package main\n\n// File %d\nfunc func%d() {}\n", i, i)
		if err := os.WriteFile(goFile, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		Extensions:  []string{".go"},
		Ignore:      []string{},
		Concurrency: 10, // high concurrency to stress test
		Model:       "test-model",
		Mode:        "index",
		IndexFile:   filepath.Join(dir, ".llmdoc", "index.yaml"),
	}

	provider := &mockProvider{}
	_, ch, err := Run(context.Background(), dir, cfg, provider, Options{})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	results := collect(ch)

	// All 20 files should be created (plus index file result if any)
	fileResults := 0
	for _, r := range results {
		if r.File.RelPath != "" && strings.HasSuffix(r.File.RelPath, ".go") {
			fileResults++
			if r.Status != StatusCreated {
				t.Errorf("expected StatusCreated, got %v", r.Status)
			}
		}
	}

	if fileResults != numFiles {
		t.Errorf("expected %d file results, got %d", numFiles, fileResults)
	}
}

// TestConcurrentDirectoryProcessing tests that directories are processed concurrently
// after file processing completes, without data races in the index update.
func TestConcurrentDirectoryProcessing(t *testing.T) {
	dir := t.TempDir()

	// Create 5 directories with 3 files each (15 files, 5 directories when enabled)
	const numDirs = 5
	const filesPerDir = 3
	for d := 0; d < numDirs; d++ {
		subdir := filepath.Join(dir, fmt.Sprintf("pkg%d", d))
		os.MkdirAll(subdir, 0755)
		for f := 0; f < filesPerDir; f++ {
			goFile := filepath.Join(subdir, fmt.Sprintf("file%d.go", f))
			content := fmt.Sprintf("package pkg%d\n\n// File %d\nfunc func%d() {}\n", d, f, f)
			os.WriteFile(goFile, []byte(content), 0644)
		}
	}

	cfg := &config.Config{
		Extensions:                 []string{".go"},
		Ignore:                     []string{},
		Concurrency:                4,
		Model:                      "test-model",
		Mode:                       "index",
		IndexFile:                  filepath.Join(dir, ".llmdoc", "index.yaml"),
		GenerateDirectorySummaries: true,
	}

	provider := &mockProvider{}
	_, ch, err := Run(context.Background(), dir, cfg, provider, Options{})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	results := collect(ch)

	// Count file and directory results
	fileResults := 0
	dirResults := 0
	for _, r := range results {
		if r.DirectoryPath != "" {
			dirResults++
		} else if r.File.RelPath != "" && strings.HasSuffix(r.File.RelPath, ".go") {
			fileResults++
		}
	}

	expectedFiles := numDirs * filesPerDir
	expectedDirs := numDirs

	if fileResults != expectedFiles {
		t.Errorf("expected %d file results, got %d", expectedFiles, fileResults)
	}
	if dirResults != expectedDirs {
		t.Errorf("expected %d directory results, got %d", expectedDirs, dirResults)
	}

	// Verify index has entries for all files and directories
	idxPath := filepath.Join(dir, ".llmdoc", "index.yaml")
	idx, err := index.Load(idxPath)
	if err != nil {
		t.Fatalf("failed to load index: %v", err)
	}

	if len(idx.Files) != expectedFiles {
		t.Errorf("index has %d files, expected %d", len(idx.Files), expectedFiles)
	}
	if len(idx.Directories) != expectedDirs {
		t.Errorf("index has %d directories, expected %d", len(idx.Directories), expectedDirs)
	}
}

// TestContextCancellation tests that the annotator respects context cancellation.
// When the context is cancelled, running goroutines should stop and not deadlock.
func TestContextCancellation(t *testing.T) {
	dir := t.TempDir()

	// Create multiple files
	for i := 0; i < 10; i++ {
		goFile := filepath.Join(dir, fmt.Sprintf("file%d.go", i))
		os.WriteFile(goFile, []byte("package main\nfunc main() {}"), 0644)
	}

	cfg := &config.Config{
		Extensions:  []string{".go"},
		Ignore:      []string{},
		Concurrency: 4,
		Model:       "test-model",
	}

	// Use a cancellable context with a short timeout
	ctx, cancel := context.WithCancel(context.Background())

	// Create a provider that blocks longer than the context timeout
	provider := &slowProvider{delay: 1} // blocks 1 second per call

	_, ch, err := Run(ctx, dir, cfg, provider, Options{})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// Cancel the context immediately (before results fully arrive)
	go func() {
		<-time.After(10 * time.Millisecond) // cancel almost immediately
		cancel()
	}()

	// Collect results. If context cancellation doesn't work properly,
	// this could deadlock. We use a timeout on the whole test to detect deadlock.
	done := make(chan bool)
	go func() {
		for range ch {
		}
		done <- true
	}()

	select {
	case <-done:
		// Success: context cancellation worked, results were collected (may be partial)
		t.Log("Context cancellation test passed")
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out — possible deadlock on context cancellation")
	}
}

// slowProvider is a test provider that simulates slow LLM calls.
type slowProvider struct {
	delay int // seconds to delay per call
}

func (s *slowProvider) Summarize(ctx context.Context, req llm.SummaryRequest) (string, llm.TokenUsage, error) {
	select {
	case <-ctx.Done():
		return "", llm.TokenUsage{}, ctx.Err()
	case <-time.After(time.Duration(s.delay) * time.Second):
		return "Summary", llm.TokenUsage{InputTokens: 100, OutputTokens: 20}, nil
	}
}

// TestForceFlag tests that the --force flag regenerates all files and directories
// even when their hashes haven't changed.
func TestForceFlag(t *testing.T) {
	dir := t.TempDir()

	// Create a simple directory structure
	subdir := filepath.Join(dir, "src")
	os.MkdirAll(subdir, 0755)
	os.WriteFile(filepath.Join(subdir, "main.go"), []byte("package main\nfunc main() {}"), 0644)
	os.WriteFile(filepath.Join(subdir, "utils.go"), []byte("package main\nfunc help() {}"), 0644)

	cfg := &config.Config{
		Extensions:                 []string{".go"},
		Ignore:                     []string{},
		Concurrency:                1,
		Model:                      "test-model",
		Mode:                       "index",
		IndexFile:                  filepath.Join(dir, ".llmdoc", "index.yaml"),
		GenerateDirectorySummaries: true,
		Force:                      false, // initially false
	}

	callCount := 0
	provider := &countingProvider{t: t, callCount: &callCount}

	// First run: should create 2 files + 1 directory = 3 LLM calls
	_, ch, _ := Run(context.Background(), dir, cfg, provider, Options{})
	results := collect(ch)
	firstRunCalls := callCount

	if firstRunCalls != 3 {
		t.Errorf("expected 3 LLM calls on first run, got %d", firstRunCalls)
	}

	// Verify no updates happened
	fileCount := 0
	dirCount := 0
	updateCount := 0
	for _, r := range results {
		if r.DirectoryPath != "" {
			dirCount++
		} else if r.File.RelPath != "" {
			fileCount++
			if r.Status == StatusUpdated {
				updateCount++
			}
		}
	}

	if updateCount > 0 {
		t.Errorf("expected no updates on first run, got %d", updateCount)
	}

	// Second run: without --force, should not call LLM (hashes unchanged)
	_, ch, _ = Run(context.Background(), dir, cfg, provider, Options{})
	collect(ch)
	secondRunCalls := callCount - firstRunCalls

	if secondRunCalls != 0 {
		t.Errorf("expected 0 LLM calls on second run without --force, got %d", secondRunCalls)
	}

	// Third run: with --force, should regenerate all (2 files + 1 directory = 3 LLM calls)
	cfg.Force = true
	_, ch, _ = Run(context.Background(), dir, cfg, provider, Options{})
	collect(ch)
	thirdRunCalls := callCount - firstRunCalls - secondRunCalls

	if thirdRunCalls != 3 {
		t.Errorf("expected 3 LLM calls on third run with --force, got %d", thirdRunCalls)
	}
}

// TestDirectoryPruning tests that stale directory entries are removed when
// their files no longer meet the 2+ file threshold.
func TestDirectoryPruning(t *testing.T) {
	dir := t.TempDir()

	// Create a directory with 2 files initially
	subdir := filepath.Join(dir, "src")
	os.MkdirAll(subdir, 0755)
	os.WriteFile(filepath.Join(subdir, "main.go"), []byte("package main\nfunc main() {}"), 0644)
	os.WriteFile(filepath.Join(subdir, "utils.go"), []byte("package main\nfunc help() {}"), 0644)

	cfg := &config.Config{
		Extensions:                 []string{".go"},
		Ignore:                     []string{},
		Concurrency:                1,
		Model:                      "test-model",
		Mode:                       "index",
		IndexFile:                  filepath.Join(dir, ".llmdoc", "index.yaml"),
		GenerateDirectorySummaries: true,
	}

	provider := &mockProvider{}

	// First run: 2 files → directory qualifies, gets summarized
	_, ch, _ := Run(context.Background(), dir, cfg, provider, Options{})
	results := collect(ch)

	dirCount := 0
	for _, r := range results {
		if r.DirectoryPath != "" {
			dirCount++
		}
	}

	if dirCount != 1 {
		t.Errorf("expected 1 directory result on first run, got %d", dirCount)
	}

	// Delete one file so the directory no longer qualifies
	os.Remove(filepath.Join(subdir, "utils.go"))

	// Second run: 1 file → directory should be pruned from index
	_, ch, _ = Run(context.Background(), dir, cfg, provider, Options{})
	results = collect(ch)

	dirCount = 0
	for _, r := range results {
		if r.DirectoryPath != "" {
			dirCount++
		}
	}

	if dirCount != 0 {
		t.Errorf("expected 0 directory results after file deletion, got %d", dirCount)
	}

	// Verify directory entry was removed from index
	idxPath := filepath.Join(dir, ".llmdoc", "index.yaml")
	idx, _ := index.Load(idxPath)
	if len(idx.Directories) > 0 {
		t.Errorf("expected directory entry to be pruned from index, but found %d entries", len(idx.Directories))
	}
}

// BenchmarkConcurrentProcessing benchmarks file processing with high concurrency.
func BenchmarkConcurrentProcessing(b *testing.B) {
	dir := b.TempDir()

	// Create many files
	const numFiles = 100
	for i := 0; i < numFiles; i++ {
		goFile := filepath.Join(dir, fmt.Sprintf("file%d.go", i))
		content := fmt.Sprintf("package main\nfunc func%d() {}\n", i)
		os.WriteFile(goFile, []byte(content), 0644)
	}

	cfg := &config.Config{
		Extensions:  []string{".go"},
		Ignore:      []string{},
		Concurrency: 8,
		Model:       "test-model",
		Mode:        "index",
		IndexFile:   filepath.Join(dir, ".llmdoc", "index.yaml"),
	}

	provider := &mockProvider{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, ch, _ := Run(context.Background(), dir, cfg, provider, Options{})
		collect(ch)
	}
}
