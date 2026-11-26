// Package torrent reads the v1 info hash and name of a magnet link or a .torrent file.
package torrent

import (
	"crypto/sha1"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// MaxFile is the largest .torrent file accepted.
const MaxFile = 10 << 20

// Meta is what an add needs to know before qBittorrent sees it.
type Meta struct {
	Hash string // lowercase hex v1 info hash
	Name string
}

// Magnet reads a magnet link's v1 info hash (hex or base32) and display name.
func Magnet(link string) (Meta, error) {
	link = strings.TrimSpace(link)
	if !strings.HasPrefix(strings.ToLower(link), "magnet:?") {
		return Meta{}, errors.New("that is not a magnet link")
	}
	// qBittorrent reads one link per line, so a line break would smuggle in a second torrent.
	if strings.ContainsFunc(link, unicode.IsControl) {
		return Meta{}, errors.New("paste one magnet link, without line breaks")
	}
	q, err := url.ParseQuery(link[len("magnet:?"):])
	if err != nil {
		return Meta{}, errors.New("the magnet link is malformed")
	}
	for _, xt := range q["xt"] {
		v, ok := strings.CutPrefix(strings.ToLower(xt), "urn:btih:")
		if !ok {
			continue
		}
		switch len(v) {
		case 40:
			if _, err := hex.DecodeString(v); err == nil {
				return Meta{Hash: v, Name: q.Get("dn")}, nil
			}
		case 32:
			if b, err := base32.StdEncoding.DecodeString(strings.ToUpper(v)); err == nil {
				return Meta{Hash: hex.EncodeToString(b), Name: q.Get("dn")}, nil
			}
		}
	}
	return Meta{}, errors.New("the magnet link has no v1 info hash (xt=urn:btih:…)")
}

var errBad = errors.New("that is not a valid .torrent file")

// File reads a .torrent file's v1 info hash and name. v2-only torrents are refused.
func File(data []byte) (Meta, error) {
	if len(data) > MaxFile {
		return Meta{}, errors.New("the .torrent file is larger than 10 MB")
	}
	if len(data) == 0 || data[0] != 'd' {
		return Meta{}, errBad
	}
	var info []byte
	end, err := dict(data, 0, 0, func(key string, start, end int) {
		if key == "info" {
			info = data[start:end]
		}
	})
	if err != nil || end != len(data) || info == nil || info[0] != 'd' {
		return Meta{}, errBad
	}
	var m Meta
	v1, version := false, int64(1)
	if _, err := dict(info, 0, 1, func(key string, start, end int) {
		switch key {
		case "name":
			if s, ok := str(info[start:end]); ok {
				m.Name = s
			}
		case "pieces":
			v1 = true
		case "meta version":
			if n, ok := integer(info[start:end]); ok {
				version = n
			}
		}
	}); err != nil {
		return Meta{}, errBad
	}
	if version >= 2 && !v1 {
		return Meta{}, errors.New("v2-only torrents are not supported; qBittorrent needs a v1 info hash")
	}
	if !v1 {
		return Meta{}, errBad
	}
	sum := sha1.Sum(info)
	m.Hash = hex.EncodeToString(sum[:])
	return m, nil
}

const maxDepth = 64

// dict walks the dictionary at b[i], calling fn with each key and the span of its value.
func dict(b []byte, i, depth int, fn func(key string, start, end int)) (int, error) {
	if i >= len(b) || b[i] != 'd' {
		return 0, errBad
	}
	i++
	for i < len(b) && b[i] != 'e' {
		kEnd, err := skip(b, i, depth+1)
		if err != nil {
			return 0, err
		}
		key, ok := str(b[i:kEnd])
		if !ok {
			return 0, errBad
		}
		vEnd, err := skip(b, kEnd, depth+1)
		if err != nil {
			return 0, err
		}
		fn(key, kEnd, vEnd)
		i = vEnd
	}
	if i >= len(b) {
		return 0, errBad
	}
	return i + 1, nil
}

// skip returns the end of the bencoded value at b[i].
func skip(b []byte, i, depth int) (int, error) {
	if i >= len(b) || depth > maxDepth {
		return 0, errBad
	}
	switch c := b[i]; {
	case c == 'i':
		end := i + 1
		for end < len(b) && b[end] != 'e' {
			end++
		}
		if end >= len(b) {
			return 0, errBad
		}
		if _, ok := integer(b[i : end+1]); !ok {
			return 0, errBad
		}
		return end + 1, nil
	case c == 'l' || c == 'd':
		i++
		for i < len(b) && b[i] != 'e' {
			end, err := skip(b, i, depth+1)
			if err != nil {
				return 0, err
			}
			i = end
		}
		if i >= len(b) {
			return 0, errBad
		}
		return i + 1, nil
	case c >= '0' && c <= '9':
		colon := i
		for colon < len(b) && b[colon] != ':' {
			colon++
		}
		if colon >= len(b) || colon-i > 10 {
			return 0, errBad
		}
		n, err := strconv.Atoi(string(b[i:colon]))
		if err != nil || n < 0 || n > len(b)-colon-1 {
			return 0, errBad
		}
		return colon + 1 + n, nil
	}
	return 0, errBad
}

func str(b []byte) (string, bool) {
	colon := strings.IndexByte(string(b), ':')
	if colon < 1 || b[0] < '0' || b[0] > '9' {
		return "", false
	}
	return string(b[colon+1:]), true
}

func integer(b []byte) (int64, bool) {
	if len(b) < 3 || b[0] != 'i' || b[len(b)-1] != 'e' {
		return 0, false
	}
	n, err := strconv.ParseInt(string(b[1:len(b)-1]), 10, 64)
	return n, err == nil
}
