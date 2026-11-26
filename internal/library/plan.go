package library

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Item is a download or import waiting to be filed.
type Item struct {
	ID      string
	Name    string
	Torrent bool
	Hash    string
	Root    string // absolute directory holding the content
	Top     string // the content's file or directory name inside Root
	Videos  []string
	Subs    []string
	Sizes   map[string]int64
	Size    int64
	Added   time.Time
	Error   string
}

// Collect lists the item's videos and subtitles, relative to Root.
func (it *Item) Collect() {
	it.Videos, it.Subs, it.Sizes, it.Size = nil, nil, map[string]int64{}, 0
	top := filepath.Join(it.Root, it.Top)
	err := filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(it.Root, p)
		rel = filepath.ToSlash(rel)
		switch {
		case IsVideo(rel, info.Size()):
			it.Videos = append(it.Videos, rel)
		case IsSubtitle(rel):
			it.Subs = append(it.Subs, rel)
		default:
			return nil
		}
		it.Sizes[rel] = info.Size()
		it.Size += info.Size()
		return nil
	})
	if err != nil {
		it.Error = "cannot read the files: " + err.Error()
	}
	slices.Sort(it.Videos)
	slices.Sort(it.Subs)
}

// Display is rel without the item's own directory.
func (it Item) Display(rel string) string {
	if r, ok := strings.CutPrefix(rel, it.Top+"/"); ok {
		return r
	}
	return path.Base(rel)
}

// Roots are the library directories filing writes into.
type Roots struct{ Movies, TV string }

// Edit is the operator's description of an item: kind, title, year, and each video's episode.
type Edit struct {
	Kind     string
	Title    string
	Year     string
	Episodes []Episode // one per Item.Videos entry
}

// Episode holds a video's season and episode as typed; an empty episode skips the file.
type Episode struct{ Season, Episode string }

// Defaults guesses an Edit from the release and file names. shows are existing TV directory names,
// used to reuse a show's year.
func Defaults(item Item, shows []string) Edit {
	r := ParseRelease(item.Name)
	e := Edit{Kind: KindMovie, Title: r.Title, Year: r.Year, Episodes: make([]Episode, len(item.Videos))}
	for i, v := range item.Videos {
		fr := ParseRelease(path.Base(v))
		if fr.Episode == 0 {
			continue
		}
		if e.Kind == KindMovie && fr.Title != "" {
			e.Title, e.Year = fr.Title, fr.Year
		}
		e.Kind = KindShow
		e.Episodes[i] = Episode{strconv.Itoa(fr.Season), strconv.Itoa(fr.Episode)}
	}
	if e.Kind == KindShow && e.Year == "" {
		for _, name := range shows {
			t, y := ParseDirName(name)
			if y != "" && strings.EqualFold(t, e.Title) {
				e.Year = y
				break
			}
		}
	}
	return e
}

// FilePlan is one video's fate; an empty Dest means the file is not filed.
type FilePlan struct {
	Src     string
	Display string
	Size    int64
	Season  string
	Episode string
	Dest    string // "movies/…" or "tv/…", for display
}

// Op places one file: Src and Dst are absolute.
type Op struct{ Src, Dst string }

// Plan is an Edit resolved into file operations, or an error the operator must fix.
type Plan struct {
	Item  Item
	Edit  Edit
	Files []FilePlan
	Ops   []Op
	Error string
}

// Build resolves an edit into operations. It reports destinations that already exist.
func Build(item Item, e Edit, roots Roots) Plan {
	p := Plan{Item: item, Edit: e}
	for i, v := range item.Videos {
		fp := FilePlan{Src: v, Display: item.Display(v), Size: item.Sizes[v]}
		if i < len(e.Episodes) {
			fp.Season, fp.Episode = e.Episodes[i].Season, e.Episodes[i].Episode
		}
		p.Files = append(p.Files, fp)
	}
	fail := func(format string, args ...any) Plan {
		p.Error, p.Ops = fmt.Sprintf(format, args...), nil
		return p
	}
	switch {
	case item.Error != "":
		return fail("%s", item.Error)
	case len(item.Videos) == 0:
		return fail("no video files found")
	}
	add := func(i int, root, label, rel string) {
		src := filepath.Join(item.Root, item.Videos[i])
		dst := filepath.Join(root, filepath.FromSlash(rel))
		p.Files[i].Dest = label + "/" + rel
		p.Ops = append(p.Ops, Op{src, dst})
		p.Ops = append(p.Ops, subtitleOps(item, src, dst)...)
	}
	switch e.Kind {
	case KindMovie:
		best := 0
		for i, v := range item.Videos {
			if item.Sizes[v] > item.Sizes[item.Videos[best]] {
				best = i
			}
		}
		rel, err := MoviePath(e.Title, e.Year, path.Ext(item.Videos[best]))
		if err != nil {
			return fail("%s", strings.TrimPrefix(err.Error(), ErrName.Error()+": "))
		}
		add(best, roots.Movies, "movies", rel)
	case KindShow:
		for i, fp := range p.Files {
			if strings.TrimSpace(fp.Episode) == "" {
				continue
			}
			season, errS := strconv.Atoi(strings.TrimSpace(fp.Season))
			episode, errE := strconv.Atoi(strings.TrimSpace(fp.Episode))
			if errS != nil || errE != nil {
				return fail("%s: season and episode must be numbers", fp.Display)
			}
			rel, err := EpisodePath(e.Title, e.Year, season, episode, path.Ext(fp.Src))
			if err != nil {
				return fail("%s: %s", fp.Display, strings.TrimPrefix(err.Error(), ErrName.Error()+": "))
			}
			add(i, roots.TV, "tv", rel)
		}
		if len(p.Ops) == 0 {
			return fail("no episodes to file: give at least one file an episode number")
		}
	default:
		return fail("choose movie or show")
	}
	seen := map[string]bool{}
	for _, op := range p.Ops {
		if seen[op.Dst] {
			return fail("two files would land on %s", filepath.Base(op.Dst))
		}
		seen[op.Dst] = true
		if _, err := os.Lstat(op.Dst); !errors.Is(err, fs.ErrNotExist) {
			return fail("%s is already in the library", filepath.Base(op.Dst))
		}
	}
	return p
}

// subtitleOps carries subtitles whose name starts with the video's stem, keeping their suffix (".en.srt").
func subtitleOps(item Item, videoSrc, videoDst string) []Op {
	stem := strings.TrimSuffix(filepath.Base(videoSrc), filepath.Ext(videoSrc))
	var out []Op
	for _, sub := range item.Subs {
		base := path.Base(sub)
		if !strings.HasPrefix(base, stem) {
			continue
		}
		suffix := strings.TrimPrefix(strings.TrimSuffix(base, path.Ext(base)), stem)
		dst := strings.TrimSuffix(videoDst, filepath.Ext(videoDst)) + suffix + strings.ToLower(path.Ext(base))
		out = append(out, Op{filepath.Join(item.Root, sub), dst})
	}
	return out
}

// SortItems orders items newest first, then by name.
func SortItems(items []Item) {
	slices.SortFunc(items, func(a, b Item) int {
		return cmp.Or(b.Added.Compare(a.Added), strings.Compare(a.Name, b.Name))
	})
}
