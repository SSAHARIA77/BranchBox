package controller

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"branchbox/internal/api"
)

// Store persists sandbox records as a JSON file so that the controller can
// restart without losing track of running sandboxes.
type Store struct{ path string }

// NewStore returns a store backed by path.
func NewStore(path string) *Store { return &Store{path: path} }

// Load reads all records; a missing file means no sandboxes.
func (s *Store) Load() ([]api.Sandbox, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []api.Sandbox
	return out, json.Unmarshal(raw, &out)
}

// Save atomically replaces the stored records.
func (s *Store) Save(sandboxes map[string]*api.Sandbox) error {
	list := make([]api.Sandbox, 0, len(sandboxes))
	for _, sb := range sandboxes {
		list = append(list, *sb)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
