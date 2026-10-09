package audit

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// FileAttrs is one file's identity at record time.
type FileAttrs struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime"`
	Mode   string `json:"mode"`
}

// FileIndex maps a file path to its recorded attributes.
type FileIndex map[string]FileAttrs

// BuildFileIndex records every regular file under the roots. The walk is
// sequential; hashing runs on a fixed bounded worker pool. Unreadable files
// and subtrees are skipped.
func BuildFileIndex(roots []string) (FileIndex, error) {
	type entry struct {
		path string
		info fs.FileInfo
	}
	var entries []entry
	for _, root := range roots {
		if root == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable subtree
			}
			if d.IsDir() {
				return nil
			}
			info, infoErr := d.Info()
			if infoErr != nil || !info.Mode().IsRegular() {
				return nil
			}
			entries = append(entries, entry{path: path, info: info})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	jobs := make(chan entry)
	idx := FileIndex{}
	var mu sync.Mutex

	var wg sync.WaitGroup
	for i := 0; i < boundedWorkers(len(entries)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				sum, sumErr := sha256File(e.path)
				if sumErr != nil {
					continue
				}
				attrs := FileAttrs{
					SHA256: sum,
					Size:   e.info.Size(),
					MTime:  e.info.ModTime().Unix(),
					Mode:   fmt.Sprintf("%o", e.info.Mode().Perm()),
				}
				mu.Lock()
				idx[e.path] = attrs
				mu.Unlock()
			}
		}()
	}
	for _, e := range entries {
		jobs <- e
	}
	close(jobs)
	wg.Wait()
	return idx, nil
}

// LoadFileIndex reads an index written by SaveFileIndex.
func LoadFileIndex(path string) (FileIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read index %q: %w", path, err)
	}
	var idx FileIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse index %q: %w", path, err)
	}
	return idx, nil
}

// SaveFileIndex writes the index as owner-only JSON.
func SaveFileIndex(path string, idx FileIndex) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("encode index: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create index dir: %w", err)
		}
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write index: %w", err)
	}
	return nil
}

// DiffIndexes returns the paths that changed between two indexes: new or
// different in after, or deleted (present in before, missing in after). It is
// the ansible inventory delta of a provisioning run.
func DiffIndexes(before, after FileIndex) []string {
	out := make([]string, 0, len(after))
	for path, attrs := range after {
		if prev, ok := before[path]; !ok || prev != attrs {
			out = append(out, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// toManifestFile renders index attributes as a manifest file entry.
func (a FileAttrs) toManifestFile(path string) ManifestFile {
	return ManifestFile{
		Path:   path,
		SHA256: a.SHA256,
		Mode:   a.Mode,
		Size:   a.Size,
		MTime:  a.MTime,
	}
}

// RecordBegin saves a pre-run index. Returns the number of files indexed.
func RecordBegin(indexPath string, roots []string) (int, error) {
	idx, err := BuildFileIndex(roots)
	if err != nil {
		return 0, err
	}
	if err := SaveFileIndex(indexPath, idx); err != nil {
		return 0, err
	}
	return len(idx), nil
}

// RecordEnd updates the manifest at manifestPath with the files the run
// changed (the diff between the pre index and a fresh post index) and refreshes
// the package and service snapshots. Files deleted by the run are removed from
// tracking. Returns the counts of files added and removed.
func RecordEnd(manifestPath, indexPath string, roots []string) (added, removed int, err error) {
	pre, err := LoadFileIndex(indexPath)
	if err != nil {
		return 0, 0, err
	}
	post, err := BuildFileIndex(roots)
	if err != nil {
		return 0, 0, err
	}

	m := Manifest{SchemaVersion: 1}
	if existing, lerr := loadManifest(manifestPath); lerr == nil {
		m = existing
	}
	m.GeneratedAt = time.Now().Unix()

	byPath := make(map[string]ManifestFile, len(m.Files))
	for _, f := range m.Files {
		byPath[f.Path] = f
	}
	for _, path := range DiffIndexes(pre, post) {
		if attrs, ok := post[path]; ok {
			byPath[path] = attrs.toManifestFile(path)
			added++
		} else {
			delete(byPath, path)
			removed++
		}
	}
	m.Files = make([]ManifestFile, 0, len(byPath))
	for _, f := range byPath {
		m.Files = append(m.Files, f)
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })

	m.PackageSnapshot = ListInstalledPackages()
	m.ServiceSnapshot = ListActiveServices()

	if err := writeManifest(manifestPath, m); err != nil {
		return 0, 0, err
	}
	return added, removed, nil
}
