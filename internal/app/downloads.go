package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/media-organizer/internal/library"
	"github.com/jeiang/media-organizer/internal/qbit"
	"github.com/jeiang/media-organizer/internal/store"
	"github.com/jeiang/media-organizer/internal/torrent"
)

// AddTorrent sends one magnet link or one .torrent file to qBittorrent with the media tag
// and approves it for automatic filing.
func (a *App) AddTorrent(ctx context.Context, magnet string, file []byte, fileName string) (torrent.Meta, error) {
	if a.Qbit == nil {
		return torrent.Meta{}, errors.New("qBittorrent is not configured")
	}
	magnet = strings.TrimSpace(magnet)
	var m torrent.Meta
	var err error
	switch {
	case magnet != "" && file != nil:
		return m, errors.New("add a magnet link or a .torrent file, not both")
	case magnet != "":
		if m, err = torrent.Magnet(magnet); err == nil {
			err = a.Qbit.AddMagnet(ctx, magnet, a.Cfg.Tag)
		}
	case file != nil:
		if m, err = torrent.File(file); err == nil {
			err = a.Qbit.AddFile(ctx, cmp.Or(path.Base(fileName), "upload.torrent"), file, a.Cfg.Tag)
		}
	default:
		return m, errors.New("paste a magnet link or choose a .torrent file")
	}
	if err != nil {
		return m, err
	}
	m.Name = cmp.Or(m.Name, m.Hash)
	if err := a.approve(m.Hash, 0); err != nil {
		return m, fmt.Errorf("added, but it will wait in intake: %w", err)
	}
	if err := a.SelectFiles(ctx); err != nil {
		a.Log.Warn("select files", "error", err)
	}
	return m, nil
}

func (a *App) approve(hash string, channel int64) error {
	return a.Store.Update(func(s *store.State) error {
		s.Approved[strings.ToLower(hash)] = store.Approval{Time: time.Now(), Channel: channel}
		return nil
	})
}

// Download is a media torrent and what the organizer makes of it.
type Download struct {
	qbit.Torrent
	Status   string
	Tone     string // badge colour: accent, ok, warn, muted
	Approved bool
	Note     string
}

// Downloads lists the media torrents, newest first.
func (a *App) Downloads(ctx context.Context) ([]Download, error) {
	if a.Qbit == nil {
		return nil, errors.New("qBittorrent is not configured")
	}
	torrents, err := a.Qbit.Torrents(ctx, "", a.Cfg.Tag)
	if err != nil {
		return nil, err
	}
	var approved map[string]store.Approval
	a.Store.View(func(s *store.State) { approved = s.Approved })
	out := make([]Download, 0, len(torrents))
	for _, t := range torrents {
		if !t.HasTag(a.Cfg.Tag) {
			continue
		}
		ap, ok := approved[t.Hash]
		d := Download{Torrent: t, Approved: ok, Note: ap.Note}
		d.Status, d.Tone = status(t)
		out = append(out, d)
	}
	slices.SortFunc(out, func(x, y Download) int { return cmp.Compare(y.AddedOn, x.AddedOn) })
	return out, nil
}

func status(t qbit.Torrent) (string, string) {
	switch {
	case t.HasTag(TagFiled):
		return "Filed", "ok"
	case t.HasTag(TagIgnored):
		return "Ignored", "muted"
	case t.Progress >= 1:
		return "Waiting in intake", "warn"
	}
	switch t.State {
	case "metaDL", "forcedMetaDL":
		return "Fetching metadata", "muted"
	case "stalledDL":
		return "Stalled", "warn"
	case "queuedDL", "checkingDL", "checkingResumeData", "allocating":
		return "Queued", "muted"
	case qbit.PausedDL, qbit.StoppedDL:
		return "Paused", "muted"
	case "error", "missingFiles":
		return "Error", "warn"
	}
	return "Downloading", "accent"
}

// Exact reports why a plan needs review, or "" when it can be filed without one: a known
// title, one video for a movie with its year, and every video of a show numbered, for a
// show the library already has or with a known year.
func (a *App) Exact(p library.Plan) string {
	switch {
	case p.Error != "":
		return p.Error
	case p.Edit.Title == "":
		return "the title is unknown"
	case p.Edit.Kind == library.KindMovie && len(p.Item.Videos) != 1:
		return fmt.Sprintf("it has %d videos; a movie needs exactly one", len(p.Item.Videos))
	case p.Edit.Kind == library.KindMovie && p.Edit.Year == "":
		return "the year is unknown"
	}
	if p.Edit.Kind == library.KindShow {
		for _, f := range p.Files {
			if f.Dest == "" {
				return f.Display + " has no episode number"
			}
		}
		if p.Edit.Year == "" {
			show := strings.Split(strings.TrimPrefix(p.Files[0].Dest, "tv/"), "/")[0]
			if info, err := os.Stat(filepath.Join(a.Cfg.TV, show)); err != nil || !info.IsDir() {
				return "the year is unknown and the show is new to the library"
			}
		}
	}
	return ""
}

// AutoFile files finished, approved downloads whose plan is exact. Each is tried once: a
// download that needs review, or whose filing fails or is undone, waits in intake.
func (a *App) AutoFile(ctx context.Context) error {
	torrents, err := a.Qbit.Torrents(ctx, "completed", a.Cfg.Tag)
	if err != nil {
		return err
	}
	var approved map[string]store.Approval
	followed := map[int64]bool{}
	a.Store.View(func(s *store.State) {
		approved = s.Approved
		for _, c := range s.Channels {
			followed[c.ID] = true
		}
	})
	var errs []error
	for _, t := range torrents {
		ap, ok := approved[t.Hash]
		if !ok || ap.Tried || !waiting(a, t) || (ap.Channel != 0 && !followed[ap.Channel]) {
			continue
		}
		note := a.Exact(a.Plan(a.torrentItem(t), nil))
		if err := a.Store.Update(func(s *store.State) error {
			ap.Tried, ap.Note = true, note
			s.Approved[t.Hash] = ap
			return nil
		}); err != nil {
			errs = append(errs, err)
			continue
		}
		if note != "" {
			a.Log.Info("left in intake", "name", t.Name, "reason", note)
			continue
		}
		r, err := a.file(ctx, t.Hash, nil, true)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.Name, err))
			if r.ID == "" {
				_ = a.Store.Update(func(s *store.State) error {
					ap.Note = "automatic filing failed: " + err.Error()
					s.Approved[t.Hash] = ap
					return nil
				})
			}
			continue
		}
		a.Log.Info("filed automatically", "name", t.Name, "title", r.Title, "files", len(r.Placed))
	}
	return errors.Join(errs...)
}

// pruneApprovals drops approvals of torrents missing from qBittorrent for over an hour.
func (a *App) pruneApprovals(ctx context.Context) error {
	torrents, err := a.Qbit.Torrents(ctx, "", "")
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, t := range torrents {
		present[t.Hash] = true
	}
	return a.Store.Update(func(s *store.State) error {
		for h, ap := range s.Approved {
			if !present[h] && time.Since(ap.Time) > time.Hour {
				delete(s.Approved, h)
			}
		}
		return nil
	})
}
