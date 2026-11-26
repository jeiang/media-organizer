package library

import (
	"cmp"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// File is one video in a library entry; Rel is relative to the entry directory.
type File struct {
	Rel     string
	Size    int64
	Season  int
	Episode int
}

// Name is the file's base name.
func (f File) Name() string { return filepath.Base(f.Rel) }

// Entry is one movie or show directory in the library.
type Entry struct {
	Kind     string
	Dir      string
	Title    string
	Year     string
	Files    []File
	Size     int64
	Modified time.Time
}

// Season groups a show's episodes.
type Season struct {
	Number int
	Files  []File
}

// Seasons groups the entry's files by season, specials (0) last.
func (e Entry) Seasons() []Season {
	var out []Season
	for _, f := range e.Files {
		i := slices.IndexFunc(out, func(s Season) bool { return s.Number == f.Season })
		if i < 0 {
			out = append(out, Season{Number: f.Season})
			i = len(out) - 1
		}
		out[i].Files = append(out[i].Files, f)
	}
	slices.SortFunc(out, func(a, b Season) int {
		if (a.Number == 0) != (b.Number == 0) {
			if a.Number == 0 {
				return 1
			}
			return -1
		}
		return cmp.Compare(a.Number, b.Number)
	})
	return out
}

// Scan lists the entries under the movies and TV roots. A missing root is empty.
func Scan(moviesDir, tvDir string) ([]Entry, error) {
	movies, err := scanRoot(moviesDir, KindMovie)
	if err != nil {
		return nil, err
	}
	shows, err := scanRoot(tvDir, KindShow)
	if err != nil {
		return nil, err
	}
	return append(movies, shows...), nil
}

func scanRoot(root, kind string) ([]Entry, error) {
	if root == "" {
		return nil, nil
	}
	dirs, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, d := range dirs {
		if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			continue
		}
		e := scanEntry(filepath.Join(root, d.Name()), kind)
		if len(e.Files) > 0 {
			out = append(out, e)
		}
	}
	return out, nil
}

func scanEntry(dir, kind string) Entry {
	e := Entry{Kind: kind, Dir: filepath.Base(dir)}
	e.Title, e.Year = ParseDirName(e.Dir)
	// Unreadable subtrees are skipped; the rest of the entry is still useful.
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !IsVideo(p, info.Size()) {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		f := File{Rel: filepath.ToSlash(rel), Size: info.Size()}
		if kind == KindShow {
			r := ParseRelease(d.Name())
			f.Season, f.Episode = r.Season, r.Episode
			if r.Episode == 0 {
				if n, ok := SeasonDir(filepath.Base(filepath.Dir(p))); ok {
					f.Season = n
				}
			}
		}
		e.Files = append(e.Files, f)
		e.Size += info.Size()
		if info.ModTime().After(e.Modified) {
			e.Modified = info.ModTime()
		}
		return nil
	})
	slices.SortFunc(e.Files, func(a, b File) int {
		return cmp.Or(cmp.Compare(a.Season, b.Season), cmp.Compare(a.Episode, b.Episode), strings.Compare(a.Rel, b.Rel))
	})
	return e
}
