// Package qbit is a small qBittorrent Web API client authenticated with an API key.
package qbit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const (
	StoppedUP = "stoppedUP"
	StoppedDL = "stoppedDL"
	PausedDL  = "pausedDL"

	PrioritySkip   = 0
	PriorityNormal = 1
)

// Client talks to one qBittorrent instance.
type Client struct {
	Base string
	Key  string
	HTTP *http.Client
}

// Torrent is the subset of torrents/info the organizer reads.
type Torrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	State       string  `json:"state"`
	Tags        string  `json:"tags"`
	ContentPath string  `json:"content_path"`
	Progress    float64 `json:"progress"`
	Size        int64   `json:"size"`
	AddedOn     int64   `json:"added_on"`
	CompletedOn int64   `json:"completion_on"`
	DLSpeed     int64   `json:"dlspeed"`
	ETA         int64   `json:"eta"`
}

// HasTag reports whether the torrent carries tag.
func (t Torrent) HasTag(tag string) bool {
	return slices.ContainsFunc(strings.Split(t.Tags, ","), func(s string) bool { return strings.TrimSpace(s) == tag })
}

// File is one file inside a torrent.
type File struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Priority int    `json:"priority"`
}

// Error is a failed request; the message never contains the API key.
type Error struct {
	Path   string
	Status int
	Err    error
}

func (e *Error) Error() string {
	switch {
	case e.Err != nil:
		return "qBittorrent unreachable: " + e.Err.Error()
	case e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden:
		return "qBittorrent refused the API key"
	}
	return fmt.Sprintf("qBittorrent %s answered %d", e.Path, e.Status)
}

func (c *Client) do(ctx context.Context, method, path string, query, form url.Values, out any) error {
	if form == nil {
		return c.send(ctx, method, path, query, nil, "", out)
	}
	return c.send(ctx, method, path, query, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", out)
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string, out any) error {
	target := strings.TrimRight(c.Base, "/") + "/api/v2/" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return &Error{Path: path, Err: err}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return &Error{Path: path, Err: err}
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return &Error{Path: path, Err: err}
	}
	if res.StatusCode != http.StatusOK {
		return &Error{Path: path, Status: res.StatusCode}
	}
	if path == "torrents/add" && strings.TrimSpace(string(data)) == "Fails." {
		return fmt.Errorf("qBittorrent did not accept the torrent")
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("qBittorrent %s: not JSON", path)
		}
	}
	return nil
}

// Torrents lists torrents; empty arguments are not sent.
func (c *Client) Torrents(ctx context.Context, filter, tag string) ([]Torrent, error) {
	q := url.Values{}
	if filter != "" {
		q.Set("filter", filter)
	}
	if tag != "" {
		q.Set("tag", tag)
	}
	var out []Torrent
	return out, c.do(ctx, http.MethodGet, "torrents/info", q, nil, &out)
}

// Files lists a torrent's files.
func (c *Client) Files(ctx context.Context, hash string) ([]File, error) {
	var out []File
	return out, c.do(ctx, http.MethodGet, "torrents/files", url.Values{"hash": {hash}}, nil, &out)
}

// AddMagnet adds a magnet link with tags.
func (c *Client) AddMagnet(ctx context.Context, magnet, tags string) error {
	return c.do(ctx, http.MethodPost, "torrents/add", nil, url.Values{"urls": {magnet}, "tags": {tags}}, nil)
}

// AddFile uploads a .torrent file with tags.
func (c *Client) AddFile(ctx context.Context, name string, data []byte, tags string) error {
	var buf strings.Builder
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("torrents", name)
	if err == nil {
		_, err = part.Write(data)
	}
	if err == nil {
		err = w.WriteField("tags", tags)
	}
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		return err
	}
	return c.send(ctx, http.MethodPost, "torrents/add", nil, strings.NewReader(buf.String()), w.FormDataContentType(), nil)
}

// AddTags tags a torrent.
func (c *Client) AddTags(ctx context.Context, hash, tags string) error {
	return c.do(ctx, http.MethodPost, "torrents/addTags", nil, url.Values{"hashes": {hash}, "tags": {tags}}, nil)
}

// RemoveTags untags a torrent.
func (c *Client) RemoveTags(ctx context.Context, hash, tags string) error {
	return c.do(ctx, http.MethodPost, "torrents/removeTags", nil, url.Values{"hashes": {hash}, "tags": {tags}}, nil)
}

// SetPriority sets the download priority of the given file indexes.
func (c *Client) SetPriority(ctx context.Context, hash string, ids []int, priority int) error {
	if len(ids) == 0 {
		return nil
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return c.do(ctx, http.MethodPost, "torrents/filePrio", nil,
		url.Values{"hash": {hash}, "id": {strings.Join(parts, "|")}, "priority": {strconv.Itoa(priority)}}, nil)
}

// Start resumes a stopped torrent.
func (c *Client) Start(ctx context.Context, hash string) error {
	return c.do(ctx, http.MethodPost, "torrents/start", nil, url.Values{"hashes": {hash}}, nil)
}

// Delete removes a torrent, and its files when deleteFiles is set.
func (c *Client) Delete(ctx context.Context, hash string, deleteFiles bool) error {
	return c.do(ctx, http.MethodPost, "torrents/delete", nil,
		url.Values{"hashes": {hash}, "deleteFiles": {strconv.FormatBool(deleteFiles)}}, nil)
}
