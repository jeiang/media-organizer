package web

import (
	"cmp"
	"slices"
	"strings"

	"github.com/jeiang/media-organizer/internal/library"
)

func sortKey(title string) string {
	t := strings.ToLower(title)
	for _, article := range []string{"the ", "a ", "an "} {
		if rest, ok := strings.CutPrefix(t, article); ok {
			return rest
		}
	}
	return t
}

func sortEntries(entries []library.Entry, by string) {
	slices.SortStableFunc(entries, func(a, b library.Entry) int {
		if by == "recent" {
			if c := b.Modified.Compare(a.Modified); c != 0 {
				return c
			}
		}
		return cmp.Or(strings.Compare(sortKey(a.Title), sortKey(b.Title)), strings.Compare(a.Year, b.Year))
	})
}
