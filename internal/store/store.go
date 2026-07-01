// Package store persists claimed subdomains to a JSON file so they survive
// restarts and cannot be double-claimed. All access is guarded by a mutex and
// every mutation is written to disk synchronously via an atomic rename.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// ponytail: JSON file store — single-tenant, low write volume. Swap for SQLite when multi-tenant.

// ErrClaimed is returned by Claim when the subdomain is already held.
var ErrClaimed = errors.New("store: subdomain already claimed")

// Store is the set of operations for tracking claimed subdomains.
type Store interface {
	Claim(name string) error   // ErrClaimed if already held
	Release(name string) error // no-op (nil) if not held
	IsClaimed(name string) bool
	List() []string // sorted
}

// FileStore is a mutex-guarded, JSON-file-backed implementation of Store.
type FileStore struct {
	mu    sync.Mutex
	path  string
	names map[string]bool
}

// Open loads the JSON file at path (creating an empty store if absent) and
// returns a *FileStore. Every mutating call persists synchronously.
func Open(path string) (*FileStore, error) {
	names := make(map[string]bool)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &names); err != nil {
			return nil, err
		}
	case os.IsNotExist(err):
		// Missing file → empty store.
	default:
		return nil, err
	}
	return &FileStore{path: path, names: names}, nil
}

// Claim marks name as held, returning ErrClaimed if it already is.
func (s *FileStore) Claim(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.names[name] {
		return ErrClaimed
	}
	s.names[name] = true
	return s.save()
}

// Release frees name. It is a no-op (returns nil) if name is not held.
func (s *FileStore) Release(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.names, name)
	return s.save()
}

// IsClaimed reports whether name is currently held.
func (s *FileStore) IsClaimed(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.names[name]
}

// List returns the sorted set of currently claimed names.
func (s *FileStore) List() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.names))
	for name := range s.names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// save marshals the current state and writes it atomically via a temp file and
// rename. The caller must hold s.mu.
func (s *FileStore) save() error {
	data, err := json.Marshal(s.names)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.path)
}
