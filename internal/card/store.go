package card

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const storeVersion = 1

type storedFile struct {
	Version int     `json:"version"`
	Cards   []Entry `json:"cards"`
}

// Store persists imported cards in one JSON file. It is a cache of public
// data, so a missing or damaged file never prevents a node from starting.
type Store struct {
	path string

	mu      sync.Mutex
	entries map[string]Entry
	loadErr error
}

// Open loads a card store. A missing file yields an empty store. A malformed
// file yields an empty store and an error; callers may log it and continue.
func Open(path string) (*Store, error) {
	store := &Store{path: path, entries: make(map[string]Entry)}
	if path == "" {
		return store, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		store.loadErr = err
		return store, err
	}
	var file storedFile
	if err := json.Unmarshal(data, &file); err != nil {
		store.loadErr = err
		return store, errors.New("card store is not valid JSON")
	}
	if file.Version != storeVersion {
		err := fmt.Errorf("card store uses unsupported version %d", file.Version)
		store.loadErr = err
		return store, err
	}
	for _, entry := range file.Cards {
		if !validEntry(entry) {
			continue
		}
		store.entries[entry.ID] = entry
	}
	return store, nil
}

func validEntry(entry Entry) bool {
	if entry.ID == "" || entry.Card == "" || entry.AddedAt.IsZero() {
		return false
	}
	if len(entry.ServerCode) != 8 {
		return false
	}
	for _, r := range entry.ServerCode {
		if r < '0' || r > '9' {
			return false
		}
	}
	if _, err := Decode(entry.Card); err != nil {
		return false
	}
	return true
}

// List returns stored cards ordered by label and server ID.
func (s *Store) List() []Entry {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Label == out[j].Label {
			return out[i].ID < out[j].ID
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// Get returns one stored card by server ID.
func (s *Store) Get(id string) (Entry, bool) {
	if s == nil {
		return Entry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[id]
	return entry, ok
}

// Put inserts or replaces a card. The caller must have verified the entry.
func (s *Store) Put(entry Entry) error {
	if s == nil || s.path == "" {
		return errors.New("card storage is unavailable")
	}
	if !validEntry(entry) {
		return errors.New("card entry is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.entries[entry.ID]; ok && entry.AddedAt.IsZero() {
		entry.AddedAt = existing.AddedAt
	}
	next := make(map[string]Entry, len(s.entries)+1)
	for id, current := range s.entries {
		next[id] = current
	}
	next[entry.ID] = entry
	if err := s.save(next); err != nil {
		return err
	}
	s.entries = next
	return nil
}

// Remove deletes a card by server ID.
func (s *Store) Remove(id string) (bool, error) {
	if s == nil {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[id]; !ok {
		return false, nil
	}
	if s.path == "" {
		return false, errors.New("card storage is unavailable")
	}
	next := make(map[string]Entry, len(s.entries))
	for currentID, entry := range s.entries {
		if currentID != id {
			next[currentID] = entry
		}
	}
	if err := s.save(next); err != nil {
		return false, err
	}
	s.entries = next
	return true, nil
}

func (s *Store) save(entries map[string]Entry) error {
	if s.path == "" {
		return errors.New("card storage is unavailable")
	}
	file := storedFile{Version: storeVersion, Cards: make([]Entry, 0, len(entries))}
	for _, entry := range entries {
		file.Cards = append(file.Cards, entry)
	}
	sort.Slice(file.Cards, func(i, j int) bool { return file.Cards[i].ID < file.Cards[j].ID })
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cards-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return nil
}
