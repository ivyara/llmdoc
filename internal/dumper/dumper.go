package dumper

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tristanmatthias/llmdoc/internal/comment"
	"github.com/tristanmatthias/llmdoc/internal/config"
	"github.com/tristanmatthias/llmdoc/internal/index"
	"github.com/tristanmatthias/llmdoc/internal/scanner"
)

// Entry holds the parsed information for one file.
type Entry struct {
	File    scanner.FileInfo
	Summary string // empty if not annotated
	Hash    string
	Content string // raw file content (only populated when includeContent is true)
}

// DirectoryEntry holds parsed information for a directory.
type DirectoryEntry struct {
	RelPath string // relative path with trailing slash (e.g., "internal/scanner/")
	Summary string // multi-line bullet points
	Hash    string
}

// Options controls dump output.
type Options struct {
	Format         string // "markdown", "xml", "plain"
	IncludeContent bool
	NoTree         bool
	Output         string // file path or "" for stdout
	Directory      string // optional scoping path (e.g., "internal/scanner")
}

// Run collects all annotated files under root and writes the summary document.
func Run(root string, cfg *config.Config, opts Options) error {
	files, err := scanner.Walk(root, cfg)
	if err != nil {
		return fmt.Errorf("scanning %s: %w", root, err)
	}

	entries, err := loadEntries(files, cfg, opts.IncludeContent)
	if err != nil {
		return err
	}

	// Load directories if available
	directories, _ := loadDirectories(cfg)

	// Apply directory scoping if requested
	if opts.Directory != "" {
		entries = filterByScope(entries, opts.Directory)
		directories = filterDirectoriesByScope(directories, opts.Directory)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].File.RelPath < entries[j].File.RelPath
	})
	sort.Slice(directories, func(i, j int) bool {
		return directories[i].RelPath < directories[j].RelPath
	})

	var w io.Writer = os.Stdout
	if opts.Output != "" {
		f, err := os.Create(opts.Output)
		if err != nil {
			return fmt.Errorf("creating output file: %w", err)
		}
		defer f.Close()
		w = f
	}

	annotated := 0
	for _, e := range entries {
		if e.Summary != "" {
			annotated++
		}
	}

	switch opts.Format {
	case "xml":
		return renderXML(w, root, entries, directories, annotated, opts)
	case "plain":
		return renderPlain(w, root, entries, directories, annotated, opts)
	default:
		return renderMarkdown(w, root, entries, directories, annotated, opts)
	}
}

// nowRFC3339 returns the current UTC time formatted as RFC3339.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// loadEntries reads annotation data for all files, using the configured storage mode.
func loadEntries(files []scanner.FileInfo, cfg *config.Config, includeContent bool) ([]Entry, error) {
	entries := make([]Entry, 0, len(files))

	if cfg.Mode == "index" {
		idx, err := index.Load(cfg.IndexFile)
		if err != nil {
			return nil, fmt.Errorf("loading index: %w", err)
		}
		for _, f := range files {
			entry := Entry{File: f}
			if e := idx.Files[f.RelPath]; e != nil {
				entry.Summary = e.Summary
				entry.Hash = e.Hash
			}
			if includeContent {
				if raw, err := os.ReadFile(f.AbsPath); err == nil {
					entry.Content = string(raw)
				}
			}
			entries = append(entries, entry)
		}
		return entries, nil
	}

	// Inline mode: read summaries from file headers.
	for _, f := range files {
		raw, err := os.ReadFile(f.AbsPath)
		if err != nil {
			continue
		}
		entry := Entry{File: f}
		if block, _ := comment.Parse(string(raw), f.Syntax); block != nil {
			entry.Summary = block.Summary
			entry.Hash = block.ContentHash
		}
		if includeContent {
			entry.Content = string(raw)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// loadDirectories loads directory summaries from the index.
// Directory summaries are only stored in index mode.
func loadDirectories(cfg *config.Config) ([]DirectoryEntry, error) {
	if cfg.Mode != "index" {
		// Directory summaries are only available in index mode
		return []DirectoryEntry{}, nil
	}

	idx, err := index.Load(cfg.IndexFile)
	if err != nil {
		return []DirectoryEntry{}, nil // silently skip if index doesn't load
	}

	var dirs []DirectoryEntry
	for path, entry := range idx.Directories {
		if entry != nil && entry.Summary != "" {
			dirs = append(dirs, DirectoryEntry{
				RelPath: path,
				Summary: entry.Summary,
				Hash:    entry.Hash,
			})
		}
	}
	return dirs, nil
}

// filterByScope filters entries to only include files within the specified scope.
func filterByScope(entries []Entry, scope string) []Entry {
	scope = filepath.ToSlash(scope)
	if !strings.HasSuffix(scope, "/") {
		scope += "/"
	}

	var filtered []Entry
	for _, e := range entries {
		path := filepath.ToSlash(e.File.RelPath)
		// Include if path is under scope or is scope itself (without trailing slash)
		if strings.HasPrefix(path, scope) || path == strings.TrimSuffix(scope, "/") {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// filterDirectoriesByScope filters directories to only include those within the specified scope.
func filterDirectoriesByScope(dirs []DirectoryEntry, scope string) []DirectoryEntry {
	scope = filepath.ToSlash(scope)
	if !strings.HasSuffix(scope, "/") {
		scope += "/"
	}

	var filtered []DirectoryEntry
	for _, d := range dirs {
		// Directory paths end with "/" - compare accordingly
		// Include if directory matches scope or is under scope
		dirPath := d.RelPath
		if dirPath == scope || strings.HasPrefix(dirPath, scope) {
			filtered = append(filtered, d)
		}
	}
	return filtered
}

// renderMarkdown writes the Markdown dump format.
func renderMarkdown(w io.Writer, root string, entries []Entry, directories []DirectoryEntry, annotated int, opts Options) error {
	abs, _ := filepath.Abs(root)
	fmt.Fprintf(w, "# Codebase Summary\n\n")
	fmt.Fprintf(w, "Generated: %s | Root: %s | %d / %d files annotated\n\n",
		nowRFC3339(), abs, annotated, len(entries))

	if !opts.NoTree {
		fmt.Fprintf(w, "## Directory Tree\n\n```\n")
		fmt.Fprintln(w, buildTree(entries))
		fmt.Fprintf(w, "```\n\n---\n\n")
	}

	// Directory summaries section
	if len(directories) > 0 {
		fmt.Fprintf(w, "## Directory Summaries\n\n")
		for _, d := range directories {
			// Strip trailing slash for display (stored as "path/" internally)
			// Note: Internal representation uses trailing slashes in index keys for consistent
			// directory identification; display removes them for user-friendly readability
			displayPath := strings.TrimSuffix(d.RelPath, "/")
			fmt.Fprintf(w, "### %s\n\n%s\n\n", displayPath, d.Summary)
		}
		fmt.Fprintf(w, "---\n\n")
	}

	fmt.Fprintf(w, "## File Summaries\n\n")
	for _, e := range entries {
		fmt.Fprintf(w, "### %s\n", e.File.RelPath)
		if e.Summary != "" {
			fmt.Fprintf(w, "%s\n\n", e.Summary)
		} else {
			fmt.Fprintf(w, "_Not yet annotated._\n\n")
		}
		if opts.IncludeContent {
			lang := strings.ToLower(strings.ReplaceAll(e.File.Language, " ", ""))
			fmt.Fprintf(w, "```%s\n%s\n```\n\n", lang, e.Content)
		}
	}
	return nil
}

// renderXML writes the XML dump format, optimized for LLM tool use.
func renderXML(w io.Writer, root string, entries []Entry, directories []DirectoryEntry, annotated int, opts Options) error {
	type xmlFile struct {
		XMLName  xml.Name `xml:"file"`
		Path     string   `xml:"path,attr"`
		Language string   `xml:"language,attr"`
		Hash     string   `xml:"hash,attr,omitempty"`
		Summary  string   `xml:"summary,omitempty"`
		Content  string   `xml:"content,omitempty"`
	}
	type xmlDirectory struct {
		XMLName xml.Name `xml:"directory"`
		Path    string   `xml:"path,attr"`
		Hash    string   `xml:"hash,attr,omitempty"`
		Summary string   `xml:"summary,omitempty"`
	}
	type xmlCodebase struct {
		XMLName   xml.Name       `xml:"codebase"`
		Root      string         `xml:"root,attr"`
		Generated string         `xml:"generated,attr"`
		Annotated int            `xml:"annotated,attr"`
		Total     int            `xml:"total,attr"`
		Dirs      []xmlDirectory `xml:"directory"`
		Files     []xmlFile      `xml:"file"`
	}

	cb := xmlCodebase{
		Root:      root,
		Generated: nowRFC3339(),
		Annotated: annotated,
		Total:     len(entries),
	}
	// Add directories to output (strip trailing slashes for display - they're stored internally with slashes)
	for _, d := range directories {
		xd := xmlDirectory{Path: strings.TrimSuffix(d.RelPath, "/"), Hash: d.Hash, Summary: d.Summary}
		cb.Dirs = append(cb.Dirs, xd)
	}
	for _, e := range entries {
		xf := xmlFile{Path: e.File.RelPath, Language: e.File.Language}
		if e.Summary != "" {
			xf.Hash = e.Hash
			xf.Summary = e.Summary
		}
		if opts.IncludeContent {
			xf.Content = e.Content
		}
		cb.Files = append(cb.Files, xf)
	}

	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	fmt.Fprintln(w, `<?xml version="1.0" encoding="UTF-8"?>`)
	if err := enc.Encode(cb); err != nil {
		return err
	}
	return enc.Flush()
}

// renderPlain writes a minimal plain text format.
func renderPlain(w io.Writer, root string, entries []Entry, directories []DirectoryEntry, annotated int, opts Options) error {
	fmt.Fprintf(w, "CODEBASE SUMMARY\nGenerated: %s\nRoot: %s\n%d/%d files annotated\n\n",
		nowRFC3339(), root, annotated, len(entries))

	if len(directories) > 0 {
		fmt.Fprintf(w, "DIRECTORY SUMMARIES\n\n")
		// Output directories with trailing slashes stripped for readability (stored internally with slashes)
		for _, d := range directories {
			fmt.Fprintf(w, "DIRECTORY: %s\n%s\n\n", strings.TrimSuffix(d.RelPath, "/"), d.Summary)
		}
	}

	for _, e := range entries {
		fmt.Fprintf(w, "FILE: %s (%s)\n", e.File.RelPath, e.File.Language)
		if e.Summary != "" {
			fmt.Fprintf(w, "%s\n", e.Summary)
		} else {
			fmt.Fprintf(w, "(not annotated)\n")
		}
		if opts.IncludeContent {
			fmt.Fprintf(w, "---\n%s\n---\n", e.Content)
		}
		fmt.Fprintln(w)
	}
	return nil
}

// buildTree constructs a simple ASCII directory tree from the file list.
func buildTree(entries []Entry) string {
	type node struct {
		children map[string]*node
		isFile   bool
	}
	root := &node{children: make(map[string]*node)}

	for _, e := range entries {
		cur := root
		parts := strings.Split(filepath.ToSlash(e.File.RelPath), "/")
		for i, p := range parts {
			if _, ok := cur.children[p]; !ok {
				cur.children[p] = &node{children: make(map[string]*node)}
			}
			cur.children[p].isFile = i == len(parts)-1
			cur = cur.children[p]
		}
	}

	// sorted returns the keys of m in sorted order.
	sorted := func(m map[string]*node) []string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}

	var sb strings.Builder
	var render func(n *node, prefix, name string, last bool)
	render = func(n *node, prefix, name string, last bool) {
		connector, childPrefix := "├── ", prefix+"│   "
		if last {
			connector, childPrefix = "└── ", prefix+"    "
		}
		if name != "" {
			sb.WriteString(prefix + connector + name + "\n")
		}
		keys := sorted(n.children)
		for i, k := range keys {
			render(n.children[k], childPrefix, k, i == len(keys)-1)
		}
	}

	keys := sorted(root.children)
	for i, k := range keys {
		render(root.children[k], "", k, i == len(keys)-1)
	}
	return sb.String()
}
