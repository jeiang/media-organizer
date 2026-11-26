package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jeiang/media-organizer/internal/eztv"
	"github.com/jeiang/media-organizer/internal/store"
)

const (
	patternChars = 200
	nameChars    = 120
)

// CompilePattern checks a channel's release-name pattern.
func CompilePattern(pattern string, caseSensitive bool) (*regexp.Regexp, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, errors.New("the pattern is empty")
	}
	if utf8.RuneCountInString(pattern) > patternChars {
		return nil, fmt.Errorf("the pattern is longer than %d characters", patternChars)
	}
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("the pattern is not a valid regular expression: %v", err)
	}
	return re, nil
}

// Channels lists followed shows.
func (a *App) Channels() []store.Channel {
	var out []store.Channel
	a.Store.View(func(s *store.State) { out = slices.Clone(s.Channels) })
	return out
}

// PollStatus is the last full check, and the releases recently sent to qBittorrent.
func (a *App) PollStatus() (store.PollStatus, []store.Added) {
	var st store.PollStatus
	var added []store.Added
	a.Store.View(func(s *store.State) { st, added = s.Poll, slices.Clone(s.Added) })
	return st, added
}

// Follow adds a channel. Its first check only marks existing releases as seen.
func (a *App) Follow(name, imdb, pattern string, caseSensitive bool, tmdbID int64) (store.Channel, error) {
	name = strings.Join(strings.Fields(name), " ")
	if r := []rune(name); len(r) > nameChars {
		name = string(r[:nameChars])
	}
	if name == "" {
		return store.Channel{}, errors.New("give the show a name")
	}
	digits, ok := eztv.IMDBDigits(imdb)
	if !ok {
		return store.Channel{}, fmt.Errorf("IMDb ID %q is not tt followed by 7 to 9 digits", imdb)
	}
	if _, err := CompilePattern(pattern, caseSensitive); err != nil {
		return store.Channel{}, err
	}
	var c store.Channel
	err := a.Store.Update(func(s *store.State) error {
		if slices.ContainsFunc(s.Channels, func(x store.Channel) bool { return x.IMDB == digits && x.Pattern == strings.TrimSpace(pattern) }) {
			return fmt.Errorf("%s is already followed with that pattern", name)
		}
		s.NextChannelID++
		c = store.Channel{
			ID: s.NextChannelID, Name: name, IMDB: digits, TMDBID: tmdbID,
			Pattern: strings.TrimSpace(pattern), CaseSensitive: caseSensitive, Created: time.Now(),
		}
		s.Channels = append(s.Channels, c)
		return nil
	})
	return c, err
}

// Unfollow removes a channel.
func (a *App) Unfollow(id int64) (store.Channel, error) {
	var c store.Channel
	err := a.Store.Update(func(s *store.State) error {
		i := slices.IndexFunc(s.Channels, func(x store.Channel) bool { return x.ID == id })
		if i < 0 {
			return ErrNotFound
		}
		c = s.Channels[i]
		s.Channels = slices.Delete(s.Channels, i, i+1)
		return nil
	})
	return c, err
}

// PreviewRow is a release and whether the pattern takes it; Before, Hit and After split
// the title around the first match.
type PreviewRow struct {
	eztv.Release
	Match              bool
	Before, Hit, After string
}

const releasesTTL = 5 * time.Minute

type releasesCache struct {
	mu   sync.Mutex
	byID map[string]cachedReleases
}

type cachedReleases struct {
	at       time.Time
	releases []eztv.Release
}

// cachedReleases serves repeated previews of one show from memory.
func (a *App) cachedReleases(ctx context.Context, imdb string) ([]eztv.Release, error) {
	c := &a.releases
	c.mu.Lock()
	hit, ok := c.byID[imdb]
	c.mu.Unlock()
	if ok && time.Since(hit.at) < releasesTTL {
		return hit.releases, nil
	}
	releases, err := a.EZTV.Releases(ctx, imdb)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.byID == nil {
		c.byID = map[string]cachedReleases{}
	}
	c.byID[imdb] = cachedReleases{time.Now(), releases}
	c.mu.Unlock()
	return releases, nil
}

// Preview fetches a show's releases and marks the ones the pattern matches.
func (a *App) Preview(ctx context.Context, imdb, pattern string, caseSensitive bool) ([]PreviewRow, error) {
	digits, ok := eztv.IMDBDigits(imdb)
	if !ok {
		return nil, fmt.Errorf("IMDb ID %q is not tt followed by 7 to 9 digits", imdb)
	}
	re, err := CompilePattern(pattern, caseSensitive)
	if err != nil {
		return nil, err
	}
	releases, err := a.cachedReleases(ctx, digits)
	if err != nil {
		return nil, err
	}
	out := make([]PreviewRow, len(releases))
	for i, r := range releases {
		out[i] = PreviewRow{Release: r, Before: r.Title}
		if loc := re.FindStringIndex(r.Title); loc != nil {
			out[i].Match = true
			out[i].Before, out[i].Hit, out[i].After = r.Title[:loc[0]], r.Title[loc[0]:loc[1]], r.Title[loc[1]:]
		}
	}
	slices.SortStableFunc(out, func(x, y PreviewRow) int {
		switch {
		case x.Match == y.Match:
			return 0
		case x.Match:
			return -1
		}
		return 1
	})
	return out, nil
}

// PollResult is what one check did.
type PollResult struct {
	Added  []store.Added
	Errors []string
}

// Poll checks one channel (only > 0) or all of them, adding new matching releases to
// qBittorrent. Force re-adds releases already seen.
func (a *App) Poll(ctx context.Context, only int64, force bool) PollResult {
	var res PollResult
	if a.Qbit == nil {
		res.Errors = append(res.Errors, "qBittorrent is not configured")
		return res
	}
	now := time.Now()
	approvals := map[string]int64{}
	for _, c := range a.Channels() {
		if only > 0 && c.ID != only {
			continue
		}
		re, err := CompilePattern(c.Pattern, c.CaseSensitive)
		var releases []eztv.Release
		if err == nil {
			releases, err = a.EZTV.Releases(ctx, c.IMDB)
		}
		if err != nil {
			res.Errors = append(res.Errors, c.Name+": "+err.Error())
			continue
		}
		var seen map[string]time.Time
		a.Store.View(func(s *store.State) { seen = s.Seen })
		var marks []string
		for _, r := range releases {
			if !re.MatchString(r.Title) {
				continue
			}
			if _, ok := seen[r.Hash]; ok && !force {
				continue
			}
			if c.Primed || force {
				if err := a.Qbit.AddMagnet(ctx, r.Magnet, a.Cfg.Tag); err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("%s: %s: %v", c.Name, r.Title, err))
					continue
				}
				res.Added = append(res.Added, store.Added{Time: now, Channel: c.Name, Title: r.Title, Hash: r.Hash, Size: r.Size})
				approvals[r.Hash] = c.ID
				a.Log.Info("added release", "channel", c.Name, "title", r.Title)
			}
			marks = append(marks, r.Hash)
		}
		err = a.Store.Update(func(s *store.State) error {
			for _, h := range marks {
				s.Seen[h] = now
			}
			if i := slices.IndexFunc(s.Channels, func(x store.Channel) bool { return x.ID == c.ID }); i >= 0 {
				s.Channels[i].Primed = true
			}
			return nil
		})
		if err != nil {
			res.Errors = append(res.Errors, c.Name+": "+err.Error())
		}
	}
	err := a.Store.Update(func(s *store.State) error {
		for h, t := range s.Seen {
			if now.Sub(t) > seenTTL {
				delete(s.Seen, h)
			}
		}
		s.Added = append(slices.Clone(res.Added), s.Added...)
		for h, id := range approvals {
			s.Approved[h] = store.Approval{Time: now, Channel: id}
		}
		if only == 0 {
			s.Poll = store.PollStatus{LastCheck: now, Errors: res.Errors}
		}
		return nil
	})
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
	}
	if len(res.Added) > 0 {
		if err := a.SelectFiles(ctx); err != nil {
			a.Log.Warn("select files", "error", err)
		}
	}
	return res
}

// ShowHit is a TMDB show search result.
type ShowHit struct {
	ID       int64
	Name     string
	Year     string
	Overview string
	Poster   string
}

// SearchShows looks shows up on TMDB.
func (a *App) SearchShows(ctx context.Context, q string) ([]ShowHit, error) {
	if a.TMDB == nil {
		return nil, errors.New("show search needs a TMDB token")
	}
	results, err := a.TMDB.Search(ctx, "tv", q, "")
	if err != nil {
		return nil, err
	}
	out := make([]ShowHit, 0, min(len(results), 8))
	for _, r := range results[:min(len(results), 8)] {
		out = append(out, ShowHit{r.ID, r.Label(), r.Year(), r.Overview, a.TMDB.ImageURL("w185", r.Poster)})
	}
	return out, nil
}

// PickShow reads a show's name and IMDb ID from TMDB.
func (a *App) PickShow(ctx context.Context, id int64) (ShowHit, string, error) {
	if a.TMDB == nil {
		return ShowHit{}, "", errors.New("show search needs a TMDB token")
	}
	r, err := a.TMDB.Show(ctx, id)
	if err != nil {
		return ShowHit{}, "", err
	}
	imdb, err := a.TMDB.IMDB(ctx, id)
	if err != nil {
		return ShowHit{}, "", err
	}
	return ShowHit{r.ID, r.Label(), r.Year(), r.Overview, a.TMDB.ImageURL("w185", r.Poster)}, imdb, nil
}
