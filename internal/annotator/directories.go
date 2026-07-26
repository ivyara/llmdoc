package annotator

import (
	"path/filepath"
	"sort"

	"github.com/tristanmatthias/llmdoc/internal/config"
	"github.com/tristanmatthias/llmdoc/internal/hasher"
	"github.com/tristanmatthias/llmdoc/internal/index"
	"github.com/tristanmatthias/llmdoc/internal/scanner"
)

// DirectoryToProcess holds information about a directory that needs annotation.
type DirectoryToProcess struct {
	RelPath        string // relative path WITH trailing slash (e.g., "internal/scanner/")
	Files          []scanner.FileInfo
	AggregatedHash string
}

// collectDirectories identifies directories with 2+ annotated files and computes their
// aggregated hashes. This is called after all file annotation completes.
//
// Only directories with 2 or more files are included. Single-file directories are skipped.
// Directories matching ignore patterns are excluded.
//
// The aggregated hash includes both file content hashes AND summary hashes, so changes to
// either file content or file summaries will trigger directory regeneration.
//
// Returns a list of DirectoryToProcess ready for LLM annotation.
func collectDirectories(files []scanner.FileInfo, cfg *config.Config, fileEntries map[string]*index.Entry) []DirectoryToProcess {
	// Group files by their immediate parent directory
	dirMap := make(map[string][]scanner.FileInfo)
	for _, f := range files {
		dir := filepath.Dir(f.RelPath)
		// Skip files in the root (Dir returns "." for root-level files)
		if dir == "." {
			continue
		}
		dirMap[dir] = append(dirMap[dir], f)
	}

	// Filter to directories with 2+ files and not ignored
	var dirsToProcess []DirectoryToProcess
	for dir, filesInDir := range dirMap {
		// Skip single-file directories
		if len(filesInDir) < 2 {
			continue
		}

		// Skip ignored directories (treat as directory with trailing slash for matching)
		if scanner.MatchesIgnore(dir, true, cfg.Ignore) {
			continue
		}

		// Collect file entry hashes for this directory (both content and summary hashes)
		dirFileEntryHashes := make(map[string]string)
		for _, f := range filesInDir {
			if entry, ok := fileEntries[f.RelPath]; ok && entry != nil {
				// Combine content hash and summary hash so changes to either trigger regeneration
				entryHash := entry.Hash
				if entry.Summary != "" {
					entryHash = entryHash + ":" + hasher.ComputeHash([]byte(entry.Summary))
				}
				dirFileEntryHashes[f.RelPath] = entryHash
			}
		}

		// Skip if no files have entries (shouldn't happen normally)
		if len(dirFileEntryHashes) == 0 {
			continue
		}

		// Compute aggregated hash from combined entry hashes
		aggregatedHash := hasher.ComputeDirectoryHash(dirFileEntryHashes)

		dirsToProcess = append(dirsToProcess, DirectoryToProcess{
			RelPath:        dir + "/",
			Files:          filesInDir,
			AggregatedHash: aggregatedHash,
		})
	}

	// Sort by path for determinism
	sort.Slice(dirsToProcess, func(i, j int) bool {
		return dirsToProcess[i].RelPath < dirsToProcess[j].RelPath
	})

	return dirsToProcess
}

// shouldRegenerateDirectory checks if a directory's hash has changed since the last annotation.
// Returns true if the directory should be regenerated (hash changed or doesn't exist).
func shouldRegenerateDirectory(dir string, aggregatedHash string, idx *index.Index) bool {
	if idx == nil {
		return true
	}

	existing, ok := idx.Directories[dir]
	if !ok {
		// Directory doesn't exist in index, needs generation
		return true
	}

	// Directory exists, check if hash changed
	return existing.Hash != aggregatedHash
}
