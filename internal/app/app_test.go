package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/media-organizer/internal/library"
	"github.com/jeiang/media-organizer/internal/qbit"
	"github.com/jeiang/media-organizer/internal/store"
)

const hash = "0123456789abcdef0123456789abcdef01234567"

// fakeQbit serves one torrent and records deletions; failUntag makes removeTags fail.
type fakeQbit struct {
	mu        sync.Mutex
	torrent   qbit.Torrent
	failUntag bool
	deleted   bool
}

func (f *fakeQbit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	switch r.URL.Path {
	case "/api/v2/torrents/info":
		_ = json.NewEncoder(w).Encode([]qbit.Torrent{f.torrent})
	case "/api/v2/torrents/addTags":
		f.torrent.Tags += "," + r.PostForm.Get("tags")
	case "/api/v2/torrents/removeTags":
		if f.failUntag {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		f.torrent.Tags = strings.ReplaceAll(f.torrent.Tags, ","+r.PostForm.Get("tags"), "")
	case "/api/v2/torrents/delete":
		f.deleted = true
	}
}

func setup(t *testing.T) (*App, *fakeQbit) {
	t.Helper()
	base := t.TempDir()
	dl := filepath.Join(base, "downloads")
	content := filepath.Join(dl, "Film.2020.1080p.mkv")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(content, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fq := &fakeQbit{torrent: qbit.Torrent{Hash: hash, Name: "Film.2020.1080p", State: qbit.StoppedUP, Tags: "media", ContentPath: content}}
	srv := httptest.NewServer(fq)
	t.Cleanup(srv.Close)
	st, err := store.Open(filepath.Join(base, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &App{
		Cfg:   Config{Movies: filepath.Join(base, "movies"), TV: filepath.Join(base, "tv"), Downloads: dl, Tag: "media"},
		Store: st,
		Qbit:  &qbit.Client{Base: srv.URL, HTTP: srv.Client()},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return a, fq
}

func TestFailedUntagLeavesFilingInPlace(t *testing.T) {
	a, fq := setup(t)
	ctx := context.Background()
	r, err := a.File(ctx, hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	fq.failUntag = true
	if _, err := a.Undo(ctx, r.ID); err == nil {
		t.Fatal("undo succeeded although the torrent kept its filed tag")
	}
	if _, err := os.Stat(r.Placed[0].Dst); err != nil {
		t.Fatalf("library copy removed while the torrent is still tagged filed: %v", err)
	}
}

// A torrent still tagged filed after its filing was undone must not be swept: its download is the only copy.
func TestSweepSkipsUndoneFiling(t *testing.T) {
	a, fq := setup(t)
	ctx := context.Background()
	r, err := a.File(ctx, hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Undo(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	fq.torrent.Tags += "," + TagFiled // tag reappears, e.g. set by hand
	if err := a.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if fq.deleted {
		t.Fatal("sweep deleted a download whose filing was undone")
	}
}

func TestSweepRemovesFiledTorrent(t *testing.T) {
	a, fq := setup(t)
	ctx := context.Background()
	if _, err := a.File(ctx, hash, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if !fq.deleted {
		t.Fatal("sweep kept a filed torrent that finished seeding")
	}
}

func TestRemoveEmptyDirKeepsFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "Show.S01E01.mkv")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = library.RemoveEmptyDir(p)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("a regular file at a reused import path was removed: %v", err)
	}
}

// An approved download that was filed by hand and then undone must stay in intake.
func TestUndoneManualFilingIsNotRefiled(t *testing.T) {
	a, _ := setup(t)
	ctx := context.Background()
	if err := a.approve(hash, 0); err != nil {
		t.Fatal(err)
	}
	r, err := a.File(ctx, hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Undo(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.AutoFile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(a.Receipts()); n != 1 {
		t.Fatalf("%d receipts: the undone filing was filed again", n)
	}
	if _, err := os.Stat(r.Placed[0].Dst); err == nil {
		t.Fatal("the undone filing is back in the library")
	}
}

// Automatic filing happens once; after undo the download waits in intake.
func TestAutoFileOnce(t *testing.T) {
	a, _ := setup(t)
	ctx := context.Background()
	if err := a.approve(hash, 0); err != nil {
		t.Fatal(err)
	}
	if err := a.AutoFile(ctx); err != nil {
		t.Fatal(err)
	}
	rs := a.Receipts()
	if len(rs) != 1 || !rs[0].Auto {
		t.Fatalf("receipts %+v, want one automatic filing", rs)
	}
	if _, err := a.Undo(ctx, rs[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := a.AutoFile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(a.Receipts()); n != 1 {
		t.Fatalf("%d receipts: an undone automatic filing was repeated", n)
	}
}
