// Package eztv reads recent releases of a show from the EZTV API.
package eztv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var imdbRe = regexp.MustCompile(`^(?:tt)?(\d{7,9})$`)

// IMDBDigits accepts "tt0123456" or "0123456" and returns the digits.
func IMDBDigits(value string) (string, bool) {
	m := imdbRe.FindStringSubmatch(strings.TrimSpace(value))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// Release is one torrent from the feed.
type Release struct {
	Hash     string
	Title    string
	Magnet   string
	Size     int64
	Season   string
	Episode  string
	Released int64
}

// Client fetches the feed.
type Client struct {
	URL   string
	Limit int
	HTTP  *http.Client
}

// flex decodes a JSON string or number as text.
type flex string

func (f *flex) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flex(s)
		return nil
	}
	if string(b) == "null" {
		*f = ""
		return nil
	}
	*f = flex(b)
	return nil
}

// Releases returns the show's recent releases, newest first.
func (c *Client) Releases(ctx context.Context, imdb string) ([]Release, error) {
	q := url.Values{"imdb_id": {imdb}, "limit": {strconv.Itoa(c.Limit)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("EZTV: %w", err)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("EZTV unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("EZTV answered %d", res.StatusCode)
	}
	var body struct {
		Torrents []struct {
			Hash     string `json:"hash"`
			Title    string `json:"title"`
			Magnet   string `json:"magnet_url"`
			Size     flex   `json:"size_bytes"`
			Season   flex   `json:"season"`
			Episode  flex   `json:"episode"`
			Released flex   `json:"date_released_unix"`
		} `json:"torrents"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("EZTV: not JSON")
	}
	out := make([]Release, 0, len(body.Torrents))
	for _, t := range body.Torrents {
		if t.Hash == "" || !strings.HasPrefix(t.Magnet, "magnet:") {
			continue
		}
		size, _ := strconv.ParseFloat(string(t.Size), 64)
		released, _ := strconv.ParseInt(string(t.Released), 10, 64)
		out = append(out, Release{
			Hash: strings.ToLower(t.Hash), Title: t.Title, Magnet: t.Magnet, Size: int64(size),
			Season: string(t.Season), Episode: string(t.Episode), Released: released,
		})
	}
	return out, nil
}
