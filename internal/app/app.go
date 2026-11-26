// Package app holds the organizer's operations: the catalogue, intake and filing,
// followed shows, and the background jobs that keep qBittorrent tidy.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/media-organizer/internal/eztv"
	"github.com/jeiang/media-organizer/internal/library"
	"github.com/jeiang/media-organizer/internal/qbit"
	"github.com/jeiang/media-organizer/internal/store"
	"github.com/jeiang/media-organizer/internal/tmdb"
)

const (
	TagFiled    = "filed"
	TagIgnored  = "ignored"
	TagSelected = "selected"

	importPrefix = "import:"
	catalogueTTL = time.Minute
	seenTTL      = 180 * 24 * time.Hour
)

var hashRe = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)

// Config is the organizer's filesystem layout and job settings.
type Config struct {
	Movies, TV, Downloads, Import string
	Tag                           string // the qBittorrent tag that marks media torrents
	PollInterval                  time.Duration
	RemoveSeeded                  bool
	CacheDir                      string
}

// App is the organizer.
type App struct {
	Cfg   Config
	Store *store.Store
	Qbit  *qbit.Client // nil when qBittorrent is not configured
	EZTV  *eztv.Client
	TMDB  *tmdb.Client // nil when no token is configured
	Log   *slog.Logger

	catMu sync.Mutex
	cat   []library.Entry
	catAt time.Time

	fileMu   sync.Mutex
	art      artCache
	releases releasesCache
}

// ErrNotFound is a missing entry, item, receipt, or channel.
var ErrNotFound = errors.New("not found")

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *App) roots() library.Roots { return library.Roots{Movies: a.Cfg.Movies, TV: a.Cfg.TV} }

// Catalogue returns the library, rescanning when the last scan is older than a minute.
func (a *App) Catalogue(force bool) ([]library.Entry, error) {
	a.catMu.Lock()
	defer a.catMu.Unlock()
	if !force && a.cat != nil && time.Since(a.catAt) < catalogueTTL {
		return a.cat, nil
	}
	entries, err := library.Scan(a.Cfg.Movies, a.Cfg.TV)
	if err != nil {
		return nil, err
	}
	a.cat, a.catAt = entries, time.Now()
	return entries, nil
}

func (a *App) invalidate() {
	a.catMu.Lock()
	a.catAt = time.Time{}
	a.catMu.Unlock()
}

// Entry finds one library entry by kind and directory name.
func (a *App) Entry(kind, dir string) (library.Entry, error) {
	entries, err := a.Catalogue(false)
	if err != nil {
		return library.Entry{}, err
	}
	for _, e := range entries {
		if e.Kind == kind && e.Dir == dir {
			return e, nil
		}
	}
	return library.Entry{}, ErrNotFound
}

// LibraryDir maps a kind to its root.
func (a *App) LibraryDir(kind string) string {
	switch kind {
	case library.KindMovie:
		return a.Cfg.Movies
	case library.KindShow:
		return a.Cfg.TV
	}
	return ""
}

// Shows lists the show directories, for reusing a show's year.
func (a *App) Shows() []string {
	dirs, _ := os.ReadDir(a.Cfg.TV)
	var out []string
	for _, d := range dirs {
		if d.IsDir() {
			out = append(out, d.Name())
		}
	}
	return out
}

func waiting(a *App, t qbit.Torrent) bool {
	return t.HasTag(a.Cfg.Tag) && !t.HasTag(TagFiled) && !t.HasTag(TagIgnored)
}

func (a *App) torrentItem(t qbit.Torrent) library.Item {
	it := library.Item{ID: t.Hash, Name: t.Name, Torrent: true, Hash: t.Hash, Added: time.Unix(cmpOr(t.CompletedOn, t.AddedOn), 0)}
	rel, err := filepath.Rel(a.Cfg.Downloads, t.ContentPath)
	if err != nil || !filepath.IsLocal(rel) {
		it.Error = "the download is outside " + a.Cfg.Downloads
		return it
	}
	it.Root, it.Top = filepath.Dir(t.ContentPath), filepath.Base(t.ContentPath)
	it.Collect()
	return it
}

func cmpOr(a, b int64) int64 {
	if a > 0 {
		return a
	}
	return b
}

func validImportName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00") && !strings.HasPrefix(name, ".")
}

func (a *App) importItem(name string) (library.Item, bool) {
	info, err := os.Lstat(filepath.Join(a.Cfg.Import, name))
	if err != nil || !(info.IsDir() || info.Mode().IsRegular()) {
		return library.Item{}, false
	}
	it := library.Item{ID: importPrefix + name, Name: name, Root: a.Cfg.Import, Top: name, Added: info.ModTime()}
	it.Collect()
	return it, it.Error != "" || len(it.Videos) > 0
}

// Intake lists what waits to be filed. Problems reaching a source are returned as text,
// so the other source still shows.
func (a *App) Intake(ctx context.Context) ([]library.Item, []string) {
	var items []library.Item
	var problems []string
	if a.Qbit != nil {
		torrents, err := a.Qbit.Torrents(ctx, "completed", a.Cfg.Tag)
		if err != nil {
			problems = append(problems, err.Error())
		}
		for _, t := range torrents {
			if waiting(a, t) {
				items = append(items, a.torrentItem(t))
			}
		}
	}
	if a.Cfg.Import != "" {
		entries, err := os.ReadDir(a.Cfg.Import)
		if err != nil && !os.IsNotExist(err) {
			problems = append(problems, "cannot read the import folder: "+err.Error())
		}
		for _, e := range entries {
			if !validImportName(e.Name()) {
				continue
			}
			if it, ok := a.importItem(e.Name()); ok {
				items = append(items, it)
			}
		}
	}
	library.SortItems(items)
	return items, problems
}

// Item finds one waiting item by ID.
func (a *App) Item(ctx context.Context, id string) (library.Item, error) {
	if name, ok := strings.CutPrefix(id, importPrefix); ok {
		if !validImportName(name) {
			return library.Item{}, ErrNotFound
		}
		if it, ok := a.importItem(name); ok {
			return it, nil
		}
		return library.Item{}, ErrNotFound
	}
	if a.Qbit == nil || !hashRe.MatchString(id) {
		return library.Item{}, ErrNotFound
	}
	torrents, err := a.Qbit.Torrents(ctx, "completed", a.Cfg.Tag)
	if err != nil {
		return library.Item{}, err
	}
	for _, t := range torrents {
		if t.Hash == id && waiting(a, t) {
			return a.torrentItem(t), nil
		}
	}
	return library.Item{}, ErrNotFound
}

// Plan resolves an item with the operator's edit, or with guessed defaults when edit is nil.
func (a *App) Plan(item library.Item, edit *library.Edit) library.Plan {
	e := library.Defaults(item, a.Shows())
	if edit != nil {
		e = *edit
	}
	return library.Build(item, e, a.roots())
}

// File files an item. A returned receipt with an error means the files were placed but a
// follow-up step failed.
func (a *App) File(ctx context.Context, id string, edit *library.Edit) (library.Receipt, error) {
	a.fileMu.Lock()
	defer a.fileMu.Unlock()
	return a.fileLocked(ctx, id, edit, false)
}

// fileLocked files an item; the caller holds fileMu.
func (a *App) fileLocked(ctx context.Context, id string, edit *library.Edit, auto bool) (library.Receipt, error) {
	item, err := a.Item(ctx, id)
	if err != nil {
		return library.Receipt{}, err
	}
	mode := library.ModeMove
	if item.Torrent {
		mode = library.ModeLink
	}
	r, err := library.Execute(a.Plan(item, edit), mode)
	if err != nil {
		return library.Receipt{}, err
	}
	r.ID, r.Auto = newID(), auto
	a.invalidate()
	var errs []error
	if err := a.Store.Update(func(s *store.State) error {
		s.Receipts = append([]library.Receipt{r}, s.Receipts...)
		consumeApproval(s, r)
		return nil
	}); err != nil {
		errs = append(errs, fmt.Errorf("the receipt was not saved, so this filing cannot be undone: %w", err))
	}
	if item.Torrent {
		if err := a.Qbit.AddTags(ctx, item.Hash, TagFiled); err != nil {
			errs = append(errs, fmt.Errorf("could not tag the torrent as filed: %w", err))
		}
	} else {
		_ = library.RemoveEmptyDir(filepath.Join(item.Root, item.Top))
	}
	return r, errors.Join(errs...)
}

// Ignore hides a torrent from intake.
func (a *App) Ignore(ctx context.Context, id string) error {
	if a.Qbit == nil || !hashRe.MatchString(id) {
		return errors.New("only downloads can be ignored; move an import out of the import folder instead")
	}
	return a.Qbit.AddTags(ctx, id, TagIgnored)
}

// Receipts lists recent filings, newest first.
func (a *App) Receipts() []library.Receipt {
	var out []library.Receipt
	a.Store.View(func(s *store.State) { out = slices.Clone(s.Receipts) })
	return out
}

// Undo reverses a filing.
func (a *App) Undo(ctx context.Context, id string) (library.Receipt, error) {
	a.fileMu.Lock()
	defer a.fileMu.Unlock()
	var r library.Receipt
	found := false
	a.Store.View(func(s *store.State) {
		if i := slices.IndexFunc(s.Receipts, func(r library.Receipt) bool { return r.ID == id }); i >= 0 {
			r, found = s.Receipts[i], true
		}
	})
	if !found {
		return r, ErrNotFound
	}
	if r.UndoneAt != nil {
		return r, fmt.Errorf("%w: already undone", library.ErrUndo)
	}
	// Untag first: a sweep deletes filed downloads, and after undo the download is the only copy.
	untagged := r.Torrent && a.Qbit != nil
	if untagged {
		if err := a.Qbit.RemoveTags(ctx, r.Hash, TagFiled); err != nil {
			return r, fmt.Errorf("could not untag the torrent, so nothing was changed: %w", err)
		}
	}
	if err := library.Undo(r); err != nil {
		if untagged {
			err = errors.Join(err, a.Qbit.AddTags(ctx, r.Hash, TagFiled))
		}
		return r, err
	}
	a.invalidate()
	now := time.Now()
	r.UndoneAt = &now
	err := a.Store.Update(func(s *store.State) error {
		if i := slices.IndexFunc(s.Receipts, func(x library.Receipt) bool { return x.ID == id }); i >= 0 {
			s.Receipts[i].UndoneAt = &now
		}
		consumeApproval(s, r)
		return nil
	})
	return r, err
}

// filedHashes are the torrents with a filing that has not been undone.
func (a *App) filedHashes() map[string]bool {
	out := map[string]bool{}
	a.Store.View(func(s *store.State) {
		for _, r := range s.Receipts {
			if r.Torrent && r.UndoneAt == nil {
				out[r.Hash] = true
			}
		}
	})
	return out
}

// Run performs the background jobs until ctx ends.
func (a *App) Run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	var lastPoll time.Time
	a.Store.View(func(s *store.State) { lastPoll = s.Poll.LastCheck })
	for {
		if a.Qbit != nil && a.PollInterval() > 0 && time.Since(lastPoll) >= a.PollInterval() {
			res := a.Poll(ctx, 0, false)
			for _, e := range res.Errors {
				a.Log.Warn("poll", "error", e)
			}
			lastPoll = time.Now()
		}
		if a.Qbit != nil {
			if err := a.SelectFiles(ctx); err != nil {
				a.Log.Warn("select files", "error", err)
			}
			if err := a.AutoFile(ctx); err != nil {
				a.Log.Warn("auto file", "error", err)
			}
			if err := a.pruneApprovals(ctx); err != nil {
				a.Log.Warn("prune approvals", "error", err)
			}
			if a.Cfg.RemoveSeeded {
				if err := a.Sweep(ctx); err != nil {
					a.Log.Warn("sweep", "error", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// PollInterval is how often followed shows are checked; zero disables checks.
func (a *App) PollInterval() time.Duration { return a.Cfg.PollInterval }

// SelectFiles skips the non-media files of new media torrents and starts them.
func (a *App) SelectFiles(ctx context.Context) error {
	torrents, err := a.Qbit.Torrents(ctx, "", a.Cfg.Tag)
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range torrents {
		if !t.HasTag(a.Cfg.Tag) || t.HasTag(TagSelected) {
			continue
		}
		if err := a.selectOne(ctx, t); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (a *App) selectOne(ctx context.Context, t qbit.Torrent) error {
	files, err := a.Qbit.Files(ctx, t.Hash)
	if err != nil || len(files) == 0 {
		return err // metadata not fetched yet; try again next minute
	}
	wanted := func(f qbit.File) bool { return library.IsVideo(f.Name, f.Size) || library.IsSubtitle(f.Name) }
	if slices.ContainsFunc(files, wanted) {
		var keep, skip []int
		for _, f := range files {
			switch {
			case wanted(f) && f.Priority == qbit.PrioritySkip:
				keep = append(keep, f.Index)
			case !wanted(f) && f.Priority != qbit.PrioritySkip:
				skip = append(skip, f.Index)
			}
		}
		if err := a.Qbit.SetPriority(ctx, t.Hash, keep, qbit.PriorityNormal); err != nil {
			return err
		}
		if err := a.Qbit.SetPriority(ctx, t.Hash, skip, qbit.PrioritySkip); err != nil {
			return err
		}
	}
	if err := a.Qbit.AddTags(ctx, t.Hash, TagSelected); err != nil {
		return err
	}
	if t.State == qbit.StoppedDL || t.State == qbit.PausedDL {
		return a.Qbit.Start(ctx, t.Hash)
	}
	return nil
}

// Sweep deletes filed torrents that finished seeding. The library keeps its hardlinks.
// Only torrents with a standing receipt go, and filing and undo wait for the sweep.
func (a *App) Sweep(ctx context.Context) error {
	a.fileMu.Lock()
	defer a.fileMu.Unlock()
	torrents, err := a.Qbit.Torrents(ctx, "completed", a.Cfg.Tag)
	if err != nil {
		return err
	}
	filed := a.filedHashes()
	var errs []error
	for _, t := range torrents {
		if !t.HasTag(a.Cfg.Tag) || !t.HasTag(TagFiled) || t.State != qbit.StoppedUP || !filed[t.Hash] {
			continue
		}
		if err := a.Qbit.Delete(ctx, t.Hash, true); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.Name, err))
			continue
		}
		a.Log.Info("removed seeded torrent", "name", t.Name)
	}
	return errors.Join(errs...)
}
