// Package store keeps the organizer's state in one JSON file, rewritten atomically.
package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jeiang/media-organizer/internal/library"
)

const (
	maxReceipts = 200
	maxAdded    = 100
)

// Channel is a followed show: new EZTV releases matching Pattern are added to qBittorrent.
type Channel struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	IMDB          string    `json:"imdb"` // digits only
	TMDBID        int64     `json:"tmdb_id,omitempty"`
	Pattern       string    `json:"pattern"`
	CaseSensitive bool      `json:"case_sensitive"`
	Primed        bool      `json:"primed"` // false until the first check marks existing releases as seen
	Created       time.Time `json:"created"`
}

// Added is a release a channel sent to qBittorrent.
type Added struct {
	Time    time.Time `json:"time"`
	Channel string    `json:"channel"`
	Title   string    `json:"title"`
	Hash    string    `json:"hash"`
	Size    int64     `json:"size"`
}

// PollStatus is the outcome of the last full check of every channel.
type PollStatus struct {
	LastCheck time.Time `json:"last_check"`
	Errors    []string  `json:"errors"`
}

// State is everything the organizer persists.
type State struct {
	NextChannelID int64                `json:"next_channel_id"`
	Channels      []Channel            `json:"channels"`
	Seen          map[string]time.Time `json:"seen"`
	Receipts      []library.Receipt    `json:"receipts"` // newest first
	Added         []Added              `json:"added"`    // newest first
	Poll          PollStatus           `json:"poll"`
}

// Store guards State and its file.
type Store struct {
	mu    sync.Mutex
	path  string
	state State
}

// Open loads path, or starts empty when it does not exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	body, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(body, &s.state); err != nil {
			return nil, err
		}
	}
	if s.state.Seen == nil {
		s.state.Seen = map[string]time.Time{}
	}
	return s, nil
}

// View reads the state; fn must not keep references to it.
func (s *Store) View(fn func(*State)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.state)
}

// Update changes the state and saves it. When fn or the save fails, the state is left unchanged.
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	var draft State
	if err := json.Unmarshal(body, &draft); err != nil {
		return err
	}
	if draft.Seen == nil {
		draft.Seen = map[string]time.Time{}
	}
	if err := fn(&draft); err != nil {
		return err
	}
	if len(draft.Receipts) > maxReceipts {
		draft.Receipts = draft.Receipts[:maxReceipts]
	}
	if len(draft.Added) > maxAdded {
		draft.Added = draft.Added[:maxAdded]
	}
	if err := s.save(draft); err != nil {
		return err
	}
	s.state = draft
	return nil
}

func (s *Store) save(st State) error {
	body, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
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
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
