// Package web serves the organizer's pages.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"html"
	"html/template"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/media-organizer/internal/app"
	"github.com/jeiang/media-organizer/internal/library"
)

//go:embed templates static
var files embed.FS

// Server renders pages for one App.
type Server struct {
	app     *app.App
	log     *slog.Logger
	pages   map[string]*template.Template
	version map[string]string
}

// New parses the templates.
func New(a *app.App, log *slog.Logger) (*Server, error) {
	s := &Server{app: a, log: log, pages: map[string]*template.Template{}, version: map[string]string{}}
	static, _ := fs.Sub(files, "static")
	err := fs.WalkDir(static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(static, p)
		sum := sha256.Sum256(body)
		s.version[p] = hex.EncodeToString(sum[:6])
		return err
	})
	if err != nil {
		return nil, err
	}
	funcs := template.FuncMap{
		"static":     func(name string) string { return "/static/" + name + "?v=" + s.version[name] },
		"bytes":      humanBytes,
		"ago":        ago,
		"pathEscape": url.PathEscape,
		"lower":      strings.ToLower,
		"sub":        func(a, b int) int { return a - b },
		"placed":     placed,
		"pad":        func(n int) string { return fmt.Sprintf("%02d", n) },
		"interval":   interval,
	}
	for _, page := range []string{"library", "entry", "intake", "review", "following", "error"} {
		t, err := template.New("base.html").Funcs(funcs).ParseFS(files, "templates/base.html", "templates/icons.html", "templates/"+page+".html")
		if err != nil {
			return nil, err
		}
		s.pages[page] = t
	}
	return s, nil
}

// Handler routes every page.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.library)
	mux.HandleFunc("GET /library/{kind}/{dir}", s.entry)
	mux.HandleFunc("GET /art/{kind}/{dir}/{which}", s.art)
	mux.HandleFunc("GET /stream/{kind}/{dir}/{rel...}", s.stream)
	mux.HandleFunc("GET /intake", s.intake)
	mux.HandleFunc("GET /intake/{id}", s.review)
	mux.HandleFunc("POST /intake/{id}/file", s.file)
	mux.HandleFunc("POST /intake/{id}/ignore", s.ignore)
	mux.HandleFunc("POST /filings/{id}/undo", s.undo)
	mux.HandleFunc("GET /following", s.following)
	mux.HandleFunc("POST /following", s.follow)
	mux.HandleFunc("POST /following/check", s.check)
	mux.HandleFunc("POST /following/{id}/check", s.check)
	mux.HandleFunc("POST /following/{id}/unfollow", s.unfollow)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	static, _ := fs.Sub(files, "static")
	fileServer := http.StripPrefix("/static/", http.FileServerFS(static))
	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})
	return http.NewCrossOriginProtection().Handler(headers(mux))
}

func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https://image.tmdb.org; media-src 'self'; "+
			"style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

type flash struct{ Kind, Message string }

// page is what every template receives.
type page struct {
	Title string
	Nav   string
	Head  string // id of the element that ends the first view, for render blocking
	Flash *flash
	Data  any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, p page) {
	if c, err := r.Cookie("flash"); err == nil {
		if v, err := url.QueryUnescape(c.Value); err == nil {
			kind, msg, _ := strings.Cut(v, "|")
			p.Flash = &flash{kind, msg}
		}
		http.SetCookie(w, &http.Cookie{Name: "flash", Path: "/", MaxAge: -1})
	}
	if p.Head == "" {
		p.Head = "page-head"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := s.pages[name].Execute(w, p); err != nil {
		s.log.Error("render", "page", name, "error", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.render(w, r, status, "error", page{Title: http.StatusText(status), Data: struct {
		Status  int
		Message string
	}{status, msg}})
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to, kind, msg string) {
	if msg != "" {
		http.SetCookie(w, &http.Cookie{
			Name: "flash", Value: url.QueryEscape(kind + "|" + msg), Path: "/", MaxAge: 60,
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// --- library ---

type libraryData struct {
	Entries       []library.Entry
	Hidden        map[int]bool
	Shown, Total  int
	Movies, Shows int
	Q, Kind, Sort string
}

func (s *Server) library(w http.ResponseWriter, r *http.Request) {
	entries, err := s.app.Catalogue(false)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "The library could not be read: "+err.Error())
		return
	}
	q := r.URL.Query()
	d := libraryData{Q: strings.TrimSpace(q.Get("q")), Kind: q.Get("kind"), Sort: q.Get("sort"), Hidden: map[int]bool{}}
	if d.Kind != library.KindMovie && d.Kind != library.KindShow {
		d.Kind = ""
	}
	if d.Sort != "title" {
		d.Sort = "recent"
	}
	for _, e := range entries {
		if e.Kind == library.KindMovie {
			d.Movies++
		} else {
			d.Shows++
		}
		if d.Kind == "" || e.Kind == d.Kind {
			d.Entries = append(d.Entries, e)
		}
	}
	d.Total = len(entries)
	sortEntries(d.Entries, d.Sort)
	needle := strings.ToLower(d.Q)
	for i, e := range d.Entries {
		if needle != "" && !strings.Contains(strings.ToLower(e.Title), needle) {
			d.Hidden[i] = true
		} else {
			d.Shown++
		}
	}
	s.render(w, r, http.StatusOK, "library", page{Title: "Library", Nav: "library", Data: d})
}

type entryData struct {
	Entry   library.Entry
	Meta    app.Meta
	Seasons []library.Season
}

func (s *Server) lookup(r *http.Request) (library.Entry, error) {
	return s.app.Entry(r.PathValue("kind"), r.PathValue("dir"))
}

func (s *Server) entry(w http.ResponseWriter, r *http.Request) {
	e, err := s.lookup(r)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "That title is not in the library.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	d := entryData{Entry: e, Meta: s.app.Meta(ctx, e)}
	if e.Kind == library.KindShow {
		d.Seasons = e.Seasons()
	}
	s.render(w, r, http.StatusOK, "entry", page{Title: e.Title, Nav: "library", Data: d})
}

func (s *Server) art(w http.ResponseWriter, r *http.Request) {
	e, err := s.lookup(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	m := s.app.Meta(ctx, e)
	var file string
	switch r.PathValue("which") {
	case "poster":
		file = m.Poster
	case "backdrop":
		file = m.Backdrop
	default:
		http.NotFound(w, r)
		return
	}
	if file != "" {
		if f, err := os.Open(file); err == nil {
			defer f.Close()
			if info, err := f.Stat(); err == nil {
				w.Header().Set("Cache-Control", "public, max-age=86400")
				w.Header().Set("Content-Type", "image/jpeg")
				http.ServeContent(w, r, "", info.ModTime(), f)
				return
			}
		}
	}
	if r.PathValue("which") != "poster" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(placeholder(e.Title)))
}

// placeholder draws a poster from the title's initial on a hue derived from the title.
func placeholder(title string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(title))
	hue := h.Sum32() % 360
	initial := "?"
	for _, r := range title {
		if r != ' ' {
			initial = strings.ToUpper(string(r))
			break
		}
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 300">`+
		`<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">`+
		`<stop offset="0" stop-color="oklch(0.42 0.06 %[1]d)"/><stop offset="1" stop-color="oklch(0.24 0.04 %[2]d)"/>`+
		`</linearGradient></defs><rect width="200" height="300" fill="url(#g)"/>`+
		`<text x="100" y="172" text-anchor="middle" font-family="Iowan Old Style, Palatino, Georgia, serif" font-size="96" fill="oklch(0.92 0.03 %[1]d)" fill-opacity="0.85">%[3]s</text></svg>`,
		hue, (hue+40)%360, html.EscapeString(initial))
}

var videoTypes = map[string]string{
	".mkv": "video/x-matroska", ".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm",
	".mov": "video/quicktime", ".avi": "video/x-msvideo", ".ts": "video/mp2t", ".wmv": "video/x-ms-wmv",
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	e, err := s.lookup(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// os.Root refuses paths, including symlinks, that leave the entry's directory.
	root, err := os.OpenRoot(filepath.Join(s.app.LibraryDir(e.Kind), e.Dir))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	rel := r.PathValue("rel")
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(path.Ext(rel))
	if t, ok := videoTypes[ext]; ok {
		w.Header().Set("Content-Type", t)
	}
	if r.URL.Query().Has("download") {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(rel)}))
	}
	http.ServeContent(w, r, path.Base(rel), info.ModTime(), f)
}

// --- intake ---

type intakeData struct {
	Plans    []library.Plan
	Problems []string
	Receipts []library.Receipt
	Import   string
	Tag      string
	Qbit     bool
}

func (s *Server) intake(w http.ResponseWriter, r *http.Request) {
	items, problems := s.app.Intake(r.Context())
	d := intakeData{Problems: problems, Receipts: s.app.Receipts(), Import: s.app.Cfg.Import, Tag: s.app.Cfg.Tag, Qbit: s.app.Qbit != nil}
	for _, it := range items {
		d.Plans = append(d.Plans, s.app.Plan(it, nil))
	}
	if len(d.Receipts) > 30 {
		d.Receipts = d.Receipts[:30]
	}
	s.render(w, r, http.StatusOK, "intake", page{Title: "Intake", Nav: "intake", Data: d})
}

// editFrom reads an edit from form values, or nil when the form carries none.
func editFrom(v url.Values, videos int) *library.Edit {
	if !v.Has("kind") {
		return nil
	}
	e := &library.Edit{Kind: v.Get("kind"), Title: strings.TrimSpace(v.Get("title")), Year: strings.TrimSpace(v.Get("year"))}
	for i := range videos {
		e.Episodes = append(e.Episodes, library.Episode{
			Season: strings.TrimSpace(v.Get("s" + strconv.Itoa(i))), Episode: strings.TrimSpace(v.Get("e" + strconv.Itoa(i))),
		})
	}
	return e
}

func editQuery(e *library.Edit) url.Values {
	v := url.Values{"kind": {e.Kind}, "title": {e.Title}, "year": {e.Year}}
	for i, ep := range e.Episodes {
		v.Set("s"+strconv.Itoa(i), ep.Season)
		v.Set("e"+strconv.Itoa(i), ep.Episode)
	}
	return v
}

func (s *Server) item(w http.ResponseWriter, r *http.Request) (library.Item, bool) {
	it, err := s.app.Item(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, app.ErrNotFound):
		s.redirect(w, r, "/intake", "warn", "That item is no longer waiting.")
		return it, false
	case err != nil:
		s.fail(w, r, http.StatusBadGateway, err.Error())
		return it, false
	}
	return it, true
}

func (s *Server) review(w http.ResponseWriter, r *http.Request) {
	it, ok := s.item(w, r)
	if !ok {
		return
	}
	plan := s.app.Plan(it, editFrom(r.URL.Query(), len(it.Videos)))
	s.render(w, r, http.StatusOK, "review", page{Title: "Review " + it.Name, Nav: "intake", Data: plan})
}

func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "The form could not be read.")
		return
	}
	id := r.PathValue("id")
	it, ok := s.item(w, r)
	if !ok {
		return
	}
	edit := editFrom(r.PostForm, len(it.Videos))
	rec, err := s.app.File(r.Context(), id, edit)
	if err != nil && rec.ID == "" {
		back := "/intake/" + url.PathEscape(id)
		if edit != nil {
			back += "?" + editQuery(edit).Encode()
		}
		s.redirect(w, r, back, "error", "Not filed: "+err.Error())
		return
	}
	msg := fmt.Sprintf("Filed %s: %d %s.", rec.Title, len(rec.Placed), plural(len(rec.Placed), "file", "files"))
	if err != nil {
		s.redirect(w, r, "/intake", "warn", msg+" "+err.Error())
		return
	}
	s.redirect(w, r, "/intake", "ok", msg)
}

func (s *Server) ignore(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Ignore(r.Context(), r.PathValue("id")); err != nil {
		s.redirect(w, r, "/intake", "error", err.Error())
		return
	}
	s.redirect(w, r, "/intake", "ok", "Ignored. It stays in qBittorrent but leaves intake.")
}

func (s *Server) undo(w http.ResponseWriter, r *http.Request) {
	rec, err := s.app.Undo(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, app.ErrNotFound):
		s.redirect(w, r, "/intake", "error", "That filing is no longer on record.")
	case errors.Is(err, library.ErrUndo):
		s.redirect(w, r, "/intake", "error", "Not undone: "+strings.TrimPrefix(err.Error(), library.ErrUndo.Error()+": "))
	case err != nil && rec.UndoneAt == nil:
		s.redirect(w, r, "/intake", "error", "Not undone: "+err.Error())
	case err != nil:
		s.redirect(w, r, "/intake", "warn", "Undid "+rec.Title+", but: "+err.Error())
	default:
		s.redirect(w, r, "/intake", "ok", "Undid "+rec.Title+". It is waiting in intake again.")
	}
}

// --- following ---

type followForm struct {
	Name, IMDB, Pattern string
	CaseSensitive       bool
	TMDBID              int64
}

type followingData struct {
	Channels  any
	Status    any
	Added     any
	Interval  time.Duration
	HasTMDB   bool
	HasQbit   bool
	Q         string
	Hits      []app.ShowHit
	Picked    *app.ShowHit
	Form      followForm
	Previewed bool
	Preview   []app.PreviewRow
	Matches   int
	Error     string
}

func (s *Server) following(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st, added := s.app.PollStatus()
	if len(added) > 15 {
		added = added[:15]
	}
	d := followingData{
		Channels: s.app.Channels(), Status: st, Added: added, Interval: s.app.PollInterval(),
		HasTMDB: s.app.TMDB != nil, HasQbit: s.app.Qbit != nil, Q: strings.TrimSpace(q.Get("q")),
		Form: followForm{Name: q.Get("name"), IMDB: q.Get("imdb"), Pattern: q.Get("pattern"), CaseSensitive: q.Has("cs")},
	}
	d.Form.TMDBID, _ = strconv.ParseInt(q.Get("tmdb"), 10, 64)
	if d.Form.Pattern == "" {
		d.Form.Pattern = "1080p"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var errs []string
	if d.Q != "" && d.HasTMDB {
		hits, err := s.app.SearchShows(ctx, d.Q)
		if err != nil {
			errs = append(errs, err.Error())
		}
		d.Hits = hits
	}
	if id, err := strconv.ParseInt(q.Get("pick"), 10, 64); err == nil && id > 0 {
		hit, imdb, err := s.app.PickShow(ctx, id)
		if err != nil {
			errs = append(errs, err.Error())
		} else {
			d.Picked, d.Form.Name, d.Form.IMDB, d.Form.TMDBID = &hit, hit.Name, imdb, hit.ID
			if imdb == "" {
				errs = append(errs, "TMDB has no IMDb ID for "+hit.Name+"; find it on imdb.com and paste it in.")
			}
		}
	}
	if q.Has("preview") {
		d.Previewed = true
		rows, err := s.app.Preview(ctx, d.Form.IMDB, d.Form.Pattern, d.Form.CaseSensitive)
		if err != nil {
			errs = append(errs, err.Error())
		}
		d.Preview = rows
		for _, row := range rows {
			if row.Match {
				d.Matches++
			}
		}
	}
	d.Error = strings.Join(errs, " ")
	s.render(w, r, http.StatusOK, "following", page{Title: "Following", Nav: "following", Data: d})
}

func (s *Server) follow(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "The form could not be read.")
		return
	}
	f := r.PostForm
	tmdbID, _ := strconv.ParseInt(f.Get("tmdb"), 10, 64)
	c, err := s.app.Follow(f.Get("name"), f.Get("imdb"), f.Get("pattern"), f.Has("cs"), tmdbID)
	if err != nil {
		back := url.Values{"name": {f.Get("name")}, "imdb": {f.Get("imdb")}, "pattern": {f.Get("pattern")}, "tmdb": {f.Get("tmdb")}}
		if f.Has("cs") {
			back.Set("cs", "on")
		}
		s.redirect(w, r, "/following?"+back.Encode(), "error", "Not followed: "+err.Error())
		return
	}
	s.redirect(w, r, "/following", "ok", "Following "+c.Name+". Existing releases are skipped; new ones go to qBittorrent.")
}

func (s *Server) unfollow(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	c, err := s.app.Unfollow(id)
	if err != nil {
		s.redirect(w, r, "/following", "error", "Not unfollowed: "+err.Error())
		return
	}
	s.redirect(w, r, "/following", "ok", "Stopped following "+c.Name+".")
}

func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	res := s.app.Poll(ctx, id, false)
	kind, msg := "ok", "Checked. Nothing new."
	if n := len(res.Added); n > 0 {
		msg = fmt.Sprintf("Sent %d new %s to qBittorrent.", n, plural(n, "release", "releases"))
	}
	if len(res.Errors) > 0 {
		kind, msg = "warn", msg+" "+strings.Join(res.Errors, "; ")
	}
	s.redirect(w, r, "/following", kind, msg)
}

// --- helpers ---

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func placed(files []library.FilePlan) []library.FilePlan {
	var out []library.FilePlan
	for _, f := range files {
		if f.Dest != "" {
			out = append(out, f)
		}
	}
	return out
}

func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

func interval(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%d h", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%d min", d/time.Minute)
	}
	return d.String()
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d d ago", int(d.Hours()/24))
	}
	return t.Format("2 Jan 2006")
}
