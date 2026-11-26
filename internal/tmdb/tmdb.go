// Package tmdb looks up titles, overviews, and artwork on The Movie Database.
package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	DefaultAPI    = "https://api.themoviedb.org/3"
	DefaultImages = "https://image.tmdb.org/t/p/"
)

// Client uses a TMDB API read access token.
type Client struct {
	Token  string
	API    string
	Images string
	HTTP   *http.Client
}

// Result is a movie or show search hit.
type Result struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Name     string `json:"name"`
	Release  string `json:"release_date"`
	FirstAir string `json:"first_air_date"`
	Overview string `json:"overview"`
	Poster   string `json:"poster_path"`
	Backdrop string `json:"backdrop_path"`
}

// Label is the result's title, whichever kind it is.
func (r Result) Label() string { return cmpOr(r.Title, r.Name) }

// Year is the first four characters of the release or first-air date.
func (r Result) Year() string {
	d := cmpOr(r.Release, r.FirstAir)
	if len(d) >= 4 {
		return d[:4]
	}
	return ""
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	target := strings.TrimRight(c.API, "/") + "/" + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("TMDB unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("TMDB %s answered %d", path, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}

// Search finds movies (kind "movie") or shows (kind "tv"); year narrows the search when set.
func (c *Client) Search(ctx context.Context, kind, query, year string) ([]Result, error) {
	q := url.Values{"query": {query}, "include_adult": {"false"}}
	if year != "" {
		if kind == "movie" {
			q.Set("year", year)
		} else {
			q.Set("first_air_date_year", year)
		}
	}
	var body struct {
		Results []Result `json:"results"`
	}
	if err := c.get(ctx, "search/"+kind, q, &body); err != nil {
		return nil, err
	}
	return body.Results, nil
}

// Show reads one show.
func (c *Client) Show(ctx context.Context, id int64) (Result, error) {
	var r Result
	return r, c.get(ctx, "tv/"+strconv.FormatInt(id, 10), nil, &r)
}

// IMDB returns a show's IMDb ID ("tt…"), or "" when TMDB has none.
func (c *Client) IMDB(ctx context.Context, id int64) (string, error) {
	var body struct {
		IMDB string `json:"imdb_id"`
	}
	return body.IMDB, c.get(ctx, "tv/"+strconv.FormatInt(id, 10)+"/external_ids", nil, &body)
}

// Image downloads an image path at a size such as "w342".
func (c *Client) Image(ctx context.Context, size, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Images, "/")+"/"+size+path, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TMDB images unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB image answered %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 16<<20))
}

// ImageURL is the public URL of an image, for thumbnails in search results.
func (c *Client) ImageURL(size, path string) string {
	if path == "" {
		return ""
	}
	return strings.TrimRight(c.Images, "/") + "/" + size + path
}
