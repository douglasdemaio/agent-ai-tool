package content

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func Load(dir string) ([]Entry, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	entries := make([]Entry, 0, len(paths))
	seen := make(map[string]string, len(paths))
	for _, path := range paths {
		entry, err := LoadFile(path)
		if err != nil {
			return nil, err
		}
		if previous, clash := seen[entry.Slug]; clash {
			return nil, fmt.Errorf("%s: duplicate slug %q, already defined in %s", path, entry.Slug, previous)
		}
		seen[entry.Slug] = path
		entries = append(entries, entry)
	}
	return entries, nil
}

func LoadFile(path string) (Entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	var entry Entry
	if err := decoder.Decode(&entry); err != nil {
		return Entry{}, fmt.Errorf("%s: %w", path, err)
	}
	if decoder.More() {
		return Entry{}, fmt.Errorf("%s: trailing content after the JSON object", path)
	}
	if err := entry.Validate(); err != nil {
		return Entry{}, fmt.Errorf("%s: %w", path, err)
	}
	return entry, nil
}
