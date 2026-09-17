package archive

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidSearchQuery = errors.New("invalid search query")

const (
	SearchSortNewest = "newest"
	SearchSortOldest = "oldest"
)

const (
	maxSearchQueryLength = 200
	maxSearchTerms       = 32
)

type SearchTerm struct {
	Name     string
	Category string
}

type SearchQuery struct {
	Include []SearchTerm
	Exclude []SearchTerm
}

func ParseSearchQuery(raw string) (SearchQuery, error) {
	if len(raw) > maxSearchQueryLength {
		return SearchQuery{}, fmt.Errorf("%w: query is too long", ErrInvalidSearchQuery)
	}
	var query SearchQuery
	for _, rawToken := range strings.Fields(raw) {
		excluded := false
		token := rawToken
		if strings.HasPrefix(token, "-") {
			excluded = true
			token = strings.TrimPrefix(token, "-")
		}
		if token == "" || strings.HasPrefix(token, "-") {
			return SearchQuery{}, fmt.Errorf("%w: malformed term", ErrInvalidSearchQuery)
		}
		term, err := parseSearchTerm(token)
		if err != nil {
			return SearchQuery{}, err
		}
		if excluded {
			query.Exclude, err = appendSearchTerm(query.Exclude, term)
		} else {
			query.Include, err = appendSearchTerm(query.Include, term)
		}
		if err != nil {
			return SearchQuery{}, err
		}
		if len(query.Include)+len(query.Exclude) > maxSearchTerms {
			return SearchQuery{}, fmt.Errorf("%w: too many terms", ErrInvalidSearchQuery)
		}
	}
	for _, include := range query.Include {
		for _, exclude := range query.Exclude {
			if include.Name == exclude.Name && (include.Category == "" || exclude.Category == "" || include.Category == exclude.Category) {
				return SearchQuery{}, fmt.Errorf("%w: term %q is both included and excluded", ErrInvalidSearchQuery, include.Name)
			}
		}
	}
	return query, nil
}

func parseSearchTerm(token string) (SearchTerm, error) {
	term := SearchTerm{}
	if strings.Contains(token, ":") {
		if strings.Count(token, ":") != 1 {
			return SearchTerm{}, fmt.Errorf("%w: malformed category term", ErrInvalidSearchQuery)
		}
		parts := strings.SplitN(token, ":", 2)
		prefix, name := parts[0], parts[1]
		prefix = strings.ToLower(prefix)
		switch prefix {
		case "artist", "character", "copyright", "general", "meta":
			term.Category = prefix
		default:
			return SearchTerm{}, fmt.Errorf("%w: unknown category", ErrInvalidSearchQuery)
		}
		token = name
	}
	term.Name = normalizeTagName(token)
	if term.Name == "" || len(term.Name) > 80 {
		return SearchTerm{}, fmt.Errorf("%w: malformed tag", ErrInvalidSearchQuery)
	}
	return term, nil
}

func appendSearchTerm(terms []SearchTerm, candidate SearchTerm) ([]SearchTerm, error) {
	for i, existing := range terms {
		if existing.Name != candidate.Name {
			continue
		}
		if existing.Category != "" && candidate.Category != "" && existing.Category != candidate.Category {
			return nil, fmt.Errorf("%w: tag %q has conflicting categories", ErrInvalidSearchQuery, candidate.Name)
		}
		if existing.Category == "" {
			terms[i].Category = candidate.Category
		}
		return terms, nil
	}
	return append(terms, candidate), nil
}

func (q SearchQuery) String() string {
	parts := make([]string, 0, len(q.Include)+len(q.Exclude))
	for _, term := range q.Include {
		parts = append(parts, formatSearchTerm(term, false))
	}
	for _, term := range q.Exclude {
		parts = append(parts, formatSearchTerm(term, true))
	}
	return strings.Join(parts, " ")
}

func formatSearchTerm(term SearchTerm, excluded bool) string {
	name := term.Name
	if term.Category != "" {
		name = term.Category + ":" + name
	}
	if excluded {
		return "-" + name
	}
	return name
}

func searchPredicates(where string, args []any, query SearchQuery) (string, []any) {
	for _, term := range query.Include {
		where += ` AND EXISTS (SELECT 1 FROM post_tags pt JOIN tags t ON t.id=pt.tag_id WHERE pt.post_id=p.id AND t.name=?`
		args = append(args, term.Name)
		if term.Category != "" {
			where += ` AND t.category=?`
			args = append(args, term.Category)
		}
		where += ")"
	}
	for _, term := range query.Exclude {
		where += ` AND NOT EXISTS (SELECT 1 FROM post_tags pt JOIN tags t ON t.id=pt.tag_id WHERE pt.post_id=p.id AND t.name=?`
		args = append(args, term.Name)
		if term.Category != "" {
			where += ` AND t.category=?`
			args = append(args, term.Category)
		}
		where += ")"
	}
	return where, args
}

func searchOrder(sort string) (string, error) {
	switch sort {
	case "", SearchSortNewest:
		return "p.published_at DESC,p.id DESC", nil
	case SearchSortOldest:
		return "p.published_at ASC,p.id ASC", nil
	default:
		return "", fmt.Errorf("%w: invalid sort", ErrInvalidSearchQuery)
	}
}
