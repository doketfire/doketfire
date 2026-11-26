package query

import (
	"fmt"
	"strings"

	"github.com/doketfire/doketfire/internal/codec"
	"github.com/doketfire/doketfire/internal/collections"
)

const maxSortFields = 3

// SortField is one ordering term.
type SortField struct {
	Path string
	Desc bool
}

// ParseSort parses a sort expression such as "-created,title".
func ParseSort(s string) ([]SortField, error) {
	if s == "" {
		return nil, nil
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty sort field")
	}
	parts := strings.Split(s, ",")
	if len(parts) > maxSortFields {
		return nil, fmt.Errorf("too many sort fields (max %d)", maxSortFields)
	}
	out := make([]SortField, 0, len(parts))
	for _, raw := range parts {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, fmt.Errorf("empty sort field")
		}
		sf := SortField{}
		switch raw[0] {
		case '-':
			sf.Desc = true
			raw = raw[1:]
		case '+':
			raw = raw[1:]
		}
		if !collections.ValidPath(raw) {
			return nil, fmt.Errorf("invalid sort field %q", raw)
		}
		sf.Path = raw
		out = append(out, sf)
	}
	return out, nil
}

// SortValues extracts the values used for ordering a document.
func SortValues(d collections.Document, sort []SortField) []any {
	out := make([]any, len(sort))
	for i, f := range sort {
		out[i] = collections.GetPath(d, f.Path)
	}
	return out
}

// CompareDocuments orders two documents according to sort, falling back to the
// document id as a stable tiebreaker.
func CompareDocuments(a, b collections.Document, sort []SortField) int {
	for _, f := range sort {
		cmp := codec.Compare(collections.GetPath(a, f.Path), collections.GetPath(b, f.Path))
		if f.Desc {
			cmp = -cmp
		}
		if cmp != 0 {
			return cmp
		}
	}
	aid, bid := a.ID(), b.ID()
	if aid < bid {
		return -1
	}
	if aid > bid {
		return 1
	}
	return 0
}
