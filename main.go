// Command media-organizer serves a web app that files downloads into a movie and TV library,
// follows shows, and streams the library to the browser.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jeiang/media-organizer/internal/app"
	"github.com/jeiang/media-organizer/internal/eztv"
	"github.com/jeiang/media-organizer/internal/qbit"
	"github.com/jeiang/media-organizer/internal/store"
	"github.com/jeiang/media-organizer/internal/tmdb"
	"github.com/jeiang/media-organizer/internal/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "media-organizer:", err)
		os.Exit(1)
	}
}

func readSecret(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

func run() error {
	var (
		listen       = flag.String("listen", "127.0.0.1:8090", "address to serve on")
		stateDir     = flag.String("state-dir", envOr("STATE_DIRECTORY", "state"), "directory for state.json")
		cacheDir     = flag.String("cache-dir", envOr("CACHE_DIRECTORY", "cache"), "directory for TMDB artwork")
		movies       = flag.String("movies", "", "movie library directory")
		tv           = flag.String("tv", "", "TV library directory")
		downloads    = flag.String("downloads", "", "qBittorrent download directory, on the same filesystem as the library")
		importDir    = flag.String("import", "", "folder whose entries are moved into the library")
		qbitURL      = flag.String("qbittorrent-url", "", "qBittorrent Web UI URL; empty disables downloads and following")
		qbitKeyFile  = flag.String("qbittorrent-key-file", "", "file holding the qBittorrent API key")
		tag          = flag.String("tag", "media", "qBittorrent tag that marks media torrents")
		tmdbFile     = flag.String("tmdb-token-file", "", "file holding a TMDB read access token or API key; empty disables artwork")
		eztvURL      = flag.String("eztv-url", "https://eztvx.to/api/get-torrents", "EZTV API endpoint")
		pollInterval = flag.Duration("poll-interval", 30*time.Minute, "how often to check followed shows; 0 disables")
		removeSeeded = flag.Bool("remove-seeded", false, "delete filed torrents (and their download copy) once qBittorrent stops seeding them")
	)
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if *movies == "" || *tv == "" {
		return errors.New("-movies and -tv are required")
	}
	if *qbitURL != "" && *downloads == "" {
		return errors.New("-downloads is required with -qbittorrent-url")
	}
	for _, dir := range []string{*stateDir, *cacheDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	st, err := store.Open(filepath.Join(*stateDir, "state.json"))
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	a := &app.App{
		Cfg: app.Config{
			Movies: *movies, TV: *tv, Downloads: *downloads, Import: *importDir,
			Tag: *tag, PollInterval: *pollInterval, RemoveSeeded: *removeSeeded, CacheDir: *cacheDir,
		},
		Store: st,
		EZTV:  &eztv.Client{URL: *eztvURL, Limit: 100, HTTP: client},
		Log:   log,
	}
	if *qbitURL != "" {
		key, err := readSecret(*qbitKeyFile)
		if err != nil {
			return fmt.Errorf("qBittorrent key: %w", err)
		}
		a.Qbit = &qbit.Client{Base: *qbitURL, Key: key, HTTP: client}
	}
	if token, err := readSecret(*tmdbFile); err != nil {
		return fmt.Errorf("TMDB token: %w", err)
	} else if token != "" {
		a.TMDB = &tmdb.Client{Token: token, API: tmdb.DefaultAPI, Images: tmdb.DefaultImages, HTTP: client}
	}

	srv, err := web.New(a, log)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go a.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	log.Info("serving", "listen", *listen)
	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
