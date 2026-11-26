package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/media-organizer/internal/library"
)

const (
	missingTTL = 7 * 24 * time.Hour
	failureTTL = 10 * time.Minute
)

// Meta is what TMDB knows about a library entry. Poster and Backdrop are cached file paths.
type Meta struct {
	TMDBID   int64     `json:"tmdb_id"`
	Title    string    `json:"title"`
	Year     string    `json:"year"`
	Overview string    `json:"overview"`
	Poster   string    `json:"poster"`
	Backdrop string    `json:"backdrop"`
	Fetched  time.Time `json:"fetched"`
	Missing  bool      `json:"missing"`
}

type artCache struct {
	once     sync.Once
	sem      chan struct{}
	mu       sync.Mutex
	locks    map[string]*sync.Mutex
	failures map[string]time.Time
}

func (c *artCache) init() {
	c.once.Do(func() {
		c.sem = make(chan struct{}, 4)
		c.locks = map[string]*sync.Mutex{}
		c.failures = map[string]time.Time{}
	})
}

func (c *artCache) lock(key string) func() {
	c.mu.Lock()
	l, ok := c.locks[key]
	if !ok {
		l = &sync.Mutex{}
		c.locks[key] = l
	}
	c.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func artKey(e library.Entry) string {
	sum := sha256.Sum256([]byte(e.Kind + "\x00" + strings.ToLower(e.Title) + "\x00" + e.Year))
	return hex.EncodeToString(sum[:12])
}

// Meta returns cached TMDB data for an entry, fetching it when allowed. It never fails:
// without a token, or when TMDB is unreachable, it returns what it has.
func (a *App) Meta(ctx context.Context, e library.Entry) Meta {
	if a.Cfg.CacheDir == "" {
		return Meta{}
	}
	a.art.init()
	key := artKey(e)
	unlock := a.art.lock(key)
	defer unlock()
	metaPath := filepath.Join(a.Cfg.CacheDir, "meta", key+".json")
	var m Meta
	if body, err := os.ReadFile(metaPath); err == nil && json.Unmarshal(body, &m) == nil {
		if !m.Missing || time.Since(m.Fetched) < missingTTL || a.TMDB == nil {
			return m
		}
	}
	if a.TMDB == nil {
		return m
	}
	a.art.mu.Lock()
	failed := time.Since(a.art.failures[key]) < failureTTL
	a.art.mu.Unlock()
	if failed {
		return m
	}
	select {
	case a.art.sem <- struct{}{}:
		defer func() { <-a.art.sem }()
	case <-ctx.Done():
		return m
	}
	fresh, err := a.fetchMeta(ctx, e, key)
	if err != nil {
		a.Log.Warn("tmdb lookup", "title", e.Title, "error", err)
		a.art.mu.Lock()
		a.art.failures[key] = time.Now()
		a.art.mu.Unlock()
		return m
	}
	if body, err := json.Marshal(fresh); err == nil {
		_ = os.MkdirAll(filepath.Dir(metaPath), 0o700)
		_ = writeFileAtomic(metaPath, body)
	}
	return fresh
}

func (a *App) fetchMeta(ctx context.Context, e library.Entry, key string) (Meta, error) {
	kind := "movie"
	if e.Kind == library.KindShow {
		kind = "tv"
	}
	results, err := a.TMDB.Search(ctx, kind, e.Title, e.Year)
	if err == nil && len(results) == 0 && e.Year != "" {
		results, err = a.TMDB.Search(ctx, kind, e.Title, "")
	}
	if err != nil {
		return Meta{}, err
	}
	m := Meta{Fetched: time.Now(), Missing: true}
	if len(results) == 0 {
		return m, nil
	}
	best := results[0]
	for _, r := range results {
		if e.Year != "" && r.Year() == e.Year {
			best = r
			break
		}
	}
	m = Meta{TMDBID: best.ID, Title: best.Label(), Year: best.Year(), Overview: best.Overview, Fetched: m.Fetched}
	artDir := filepath.Join(a.Cfg.CacheDir, "art")
	if err := os.MkdirAll(artDir, 0o700); err != nil {
		return Meta{}, err
	}
	for _, img := range []struct {
		src, size string
		dst       *string
		name      string
	}{
		{best.Poster, "w342", &m.Poster, key + "-poster.jpg"},
		{best.Backdrop, "w1280", &m.Backdrop, key + "-backdrop.jpg"},
	} {
		if img.src == "" {
			continue
		}
		data, err := a.TMDB.Image(ctx, img.size, img.src)
		if err != nil {
			return Meta{}, err
		}
		p := filepath.Join(artDir, img.name)
		if err := writeFileAtomic(p, data); err != nil {
			return Meta{}, err
		}
		*img.dst = p
	}
	m.Missing = m.Poster == ""
	return m, nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
