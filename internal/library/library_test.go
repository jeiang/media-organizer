package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRelease(t *testing.T) {
	cases := []struct {
		in   string
		want Parsed
	}{
		{"Dune.Part.Two.2024.2160p.WEB-DL.mkv", Parsed{Title: "Dune Part Two", Year: "2024"}},
		{"[group] Show.Name.S02E05.1080p.mkv", Parsed{Title: "Show Name", Season: 2, Episode: 5}},
		{"Show Name 2019 S01 E10", Parsed{Title: "Show Name", Year: "2019", Season: 1, Episode: 10}},
		{"2001.A.Space.Odyssey.1968.mkv", Parsed{Title: "2001 A Space Odyssey", Year: "1968"}},
	}
	for _, c := range cases {
		if got := ParseRelease(c.in); got != c.want {
			t.Errorf("ParseRelease(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

// Release names come from torrents, so they must never escape the library root.
func TestPathsStayInsideRoot(t *testing.T) {
	for _, title := range []string{"../../etc", "..", "a/../../b", "C:\\x", "evil\x00name", " . "} {
		for _, rel := range []func() (string, error){
			func() (string, error) { return MoviePath(title, "", ".mkv") },
			func() (string, error) { return EpisodePath(title, "", 1, 1, ".mkv") },
		} {
			p, err := rel()
			if err != nil {
				continue
			}
			if !filepath.IsLocal(p) || strings.ContainsAny(p, "\\\x00") {
				t.Errorf("title %q gave unsafe path %q", title, p)
			}
			for part := range strings.SplitSeq(p, "/") {
				if part == ".." || part == "." || part == "" {
					t.Errorf("title %q gave path %q with component %q", title, p, part)
				}
			}
		}
	}
}

func setup(t *testing.T) (Item, Roots) {
	t.Helper()
	base := t.TempDir()
	roots := Roots{Movies: filepath.Join(base, "movies"), TV: filepath.Join(base, "tv")}
	dl := filepath.Join(base, "downloads")
	rel := filepath.Join("Film.2020.1080p", "Film.2020.1080p.mkv")
	if err := os.MkdirAll(filepath.Join(dl, "Film.2020.1080p"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{rel, filepath.Join("Film.2020.1080p", "Film.2020.1080p.en.srt")} {
		if err := os.WriteFile(filepath.Join(dl, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	it := Item{ID: "h", Name: "Film.2020.1080p", Torrent: true, Root: dl, Top: "Film.2020.1080p"}
	it.Collect()
	return it, roots
}

func TestFileAndUndo(t *testing.T) {
	it, roots := setup(t)
	p := Build(it, Defaults(it, nil), roots)
	if p.Error != "" {
		t.Fatal(p.Error)
	}
	r, err := Execute(p, ModeLink)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Film (2020)/Film (2020).mkv", "Film (2020)/Film (2020).en.srt"} {
		if _, err := os.Stat(filepath.Join(roots.Movies, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
	if again := Build(it, Defaults(it, nil), roots); again.Error == "" {
		t.Error("filing over an existing library file was allowed")
	}
	if err := Undo(r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(roots.Movies, "Film (2020)")); !os.IsNotExist(err) {
		t.Errorf("undo left the movie directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(it.Root, it.Videos[0])); err != nil {
		t.Errorf("undo touched the download: %v", err)
	}
}

func TestUndoRefusesToDeleteOnlyCopy(t *testing.T) {
	it, roots := setup(t)
	r, err := Execute(Build(it, Defaults(it, nil), roots), ModeLink)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(it.Root, it.Top)); err != nil {
		t.Fatal(err)
	}
	if err := Undo(r); err == nil {
		t.Fatal("undo deleted the only remaining copy")
	}
	if _, err := os.Stat(r.Placed[0].Dst); err != nil {
		t.Errorf("library copy is gone: %v", err)
	}
}

func TestMoveAndUndo(t *testing.T) {
	it, roots := setup(t)
	it.Torrent = false
	r, err := Execute(Build(it, Defaults(it, nil), roots), ModeMove)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(it.Root, it.Videos[0])
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("move left the source: %v", err)
	}
	if err := Undo(r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("undo did not restore the source: %v", err)
	}
}
