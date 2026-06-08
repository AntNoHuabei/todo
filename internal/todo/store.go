package todo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &Store{path: path}, nil
}

func (s *Store) List() ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) Save(items []Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(items)
}

func (s *Store) Update(fn func([]Item) ([]Item, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.loadLocked()
	if err != nil {
		return err
	}
	items, err = fn(items)
	if err != nil {
		return err
	}
	return s.saveLocked(items)
}

func (s *Store) loadLocked() ([]Item, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Item{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return []Item{}, nil
	}
	var items []Item
	if err := json.Unmarshal(b, &items); err != nil {
		backup := fmt.Sprintf("%s.bad-%d", s.path, time.Now().Unix())
		_ = os.Rename(s.path, backup)
		return nil, fmt.Errorf("parse todos failed; bad file moved to %s: %w", backup, err)
	}
	sortItems(items)
	return items, nil
}

func (s *Store) saveLocked(items []Item) error {
	sortItems(items)
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Status != b.Status {
			return a.Status < b.Status
		}
		if a.DueAt == nil && b.DueAt != nil {
			return false
		}
		if a.DueAt != nil && b.DueAt == nil {
			return true
		}
		if a.DueAt != nil && b.DueAt != nil && !a.DueAt.Equal(*b.DueAt) {
			return a.DueAt.Before(*b.DueAt)
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
}
