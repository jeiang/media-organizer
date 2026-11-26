// Package library parses release names, scans the library, and files
// downloads into it.
package library

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	KindMovie = "movie"
	KindShow  = "show"
)

var (
	episodeRe = regexp.MustCompile(`(?i)\bS(\d{1,2}) ?E(\d{1,3})`)
	yearRe    = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	leadTagRe = regexp.MustCompile(`^\s*\[[^\]]*\]\s*`)
	dirNameRe = regexp.MustCompile(`^(.*?)\s*\((\d{4})\)$`)
	seasonRe  = regexp.MustCompile(`(?i)^(?:season|series|s)\s*(\d{1,3})$`)

	unsafeChars = strings.NewReplacer("/", "", "\\", "", ":", " -", "*", "", "?", "", `"`, "", "<", "", ">", "", "|", "")
)

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".ts": true, ".wmv": true, ".webm": true,
}

var subtitleExts = map[string]bool{
	".srt": true, ".sub": true, ".idx": true, ".ass": true, ".ssa": true, ".vtt": true,
}

const sampleLimit = 150 << 20

// ErrName reports a title or episode number that cannot become a library path.
var ErrName = errors.New("invalid name")

func ext(name string) string { return strings.ToLower(path.Ext(name)) }

// IsVideo reports whether rel names a video worth filing; size is -1 when unknown.
func IsVideo(rel string, size int64) bool { return videoExts[ext(rel)] && !IsSample(rel, size) }

// IsSubtitle reports whether name is a subtitle file.
func IsSubtitle(name string) bool { return subtitleExts[ext(name)] }

// IsSample reports whether a small file is a release sample, by name or by a sample(s) directory.
func IsSample(rel string, size int64) bool {
	if size >= sampleLimit {
		return false
	}
	base := strings.ToLower(path.Base(rel))
	parent := strings.ToLower(path.Base(path.Dir(rel)))
	return strings.Contains(base, "sample") || parent == "sample" || parent == "samples"
}

// Parsed is what a release name says about its content.
type Parsed struct {
	Title   string
	Year    string
	Season  int
	Episode int
}

// ParseRelease reads title, year, and an SxxEyy marker from a release or file name.
func ParseRelease(name string) Parsed {
	if videoExts[ext(name)] || subtitleExts[ext(name)] {
		name = name[:len(name)-len(path.Ext(name))]
	}
	name = leadTagRe.ReplaceAllString(name, "")
	name = strings.NewReplacer(".", " ", "_", " ").Replace(name)
	var out Parsed
	cut := len(name)
	if loc := episodeRe.FindStringSubmatchIndex(name); loc != nil {
		out.Season, _ = strconv.Atoi(name[loc[2]:loc[3]])
		out.Episode, _ = strconv.Atoi(name[loc[4]:loc[5]])
		cut = loc[0]
	}
	// The last year before the marker wins, but never one that starts the name ("2001 A Space Odyssey").
	for _, loc := range yearRe.FindAllStringSubmatchIndex(name[:cut], -1) {
		if loc[0] > 0 {
			out.Year, cut = name[loc[2]:loc[3]], loc[0]
		}
	}
	out.Title = strings.Join(strings.Fields(strings.Trim(name[:cut], " -([")), " ")
	return out
}

// ParseDirName reads a library directory name such as "Title (2021)".
func ParseDirName(name string) (title, year string) {
	if m := dirNameRe.FindStringSubmatch(name); m != nil {
		return m[1], m[2]
	}
	return name, ""
}

// SeasonDir reads a "Season 01" style directory name.
func SeasonDir(name string) (int, bool) {
	m := seasonRe.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// CleanComponent makes text safe as one path component, or fails when nothing is left.
func CleanComponent(text string) (string, error) {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(unsafeChars.Replace(text)), " ")
	text = strings.Trim(text, " .")
	if text == "" {
		return "", fmt.Errorf("%w: title is empty", ErrName)
	}
	return text, nil
}

func named(title, year string) (string, error) {
	title, err := CleanComponent(title)
	if err != nil {
		return "", err
	}
	year = strings.TrimSpace(year)
	if year == "" {
		return title, nil
	}
	if len(year) != 4 || !yearRe.MatchString(year) {
		return "", fmt.Errorf("%w: year %q is not a year", ErrName, year)
	}
	return fmt.Sprintf("%s (%s)", title, year), nil
}

// MoviePath is the movie's path inside the movies root: "Title (Year)/Title (Year).ext".
func MoviePath(title, year, extension string) (string, error) {
	n, err := named(title, year)
	if err != nil {
		return "", err
	}
	return n + "/" + n + strings.ToLower(extension), nil
}

// EpisodePath is the episode's path inside the TV root: "Show (Year)/Season NN/Show (Year) - SNNENN.ext".
func EpisodePath(show, year string, season, episode int, extension string) (string, error) {
	n, err := named(show, year)
	if err != nil {
		return "", err
	}
	if season < 0 || season > 999 || episode < 1 || episode > 9999 {
		return "", fmt.Errorf("%w: season %d episode %d is not an episode", ErrName, season, episode)
	}
	return fmt.Sprintf("%s/Season %02d/%s - S%02dE%02d%s", n, season, n, season, episode, strings.ToLower(extension)), nil
}
