package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"kura/internal/archive"
)

type postContext struct {
	Source     string
	Query      string
	PoolSlug   string
	Status     string
	UploaderID int64
	Page       int
}

func browsePostContext(r *http.Request) postContext {
	return postContext{Source: "browse", Query: r.URL.Query().Get("q"), Page: pageNumber(r)}
}

func poolPostContext(slug string) postContext {
	return postContext{Source: "pool", PoolSlug: slug}
}

func uploadsPostContext(status string, page int) postContext {
	return postContext{Source: "uploads", Status: status, Page: page}
}

func adminPostContext(status string, uploaderID int64, page int) postContext {
	return postContext{Source: "admin", Status: status, UploaderID: uploaderID, Page: page}
}

func parsePostContext(r *http.Request) postContext {
	query := r.URL.Query()
	context := postContext{Source: "browse"}
	switch query.Get("context") {
	case "browse":
		if len(query.Get("q")) > 200 {
			return context
		}
		page, ok := contextPage(query.Get("page"))
		if !ok {
			return context
		}
		context.Query, context.Page = query.Get("q"), page
	case "pool":
		slug := query.Get("pool")
		if !validPoolSlug(slug) {
			return context
		}
		context.Source, context.PoolSlug = "pool", slug
	case "uploads":
		status := query.Get("status")
		if status != "all" && status != "draft" && status != "published" {
			return context
		}
		page, ok := contextPage(query.Get("page"))
		if !ok {
			return context
		}
		context.Source, context.Status, context.Page = "uploads", status, page
	case "admin":
		status := query.Get("status")
		if status != "all" && status != "draft" && status != "published" && status != "deleted" && status != "quarantined" {
			return context
		}
		uploaderID, err := parseContextUploader(query.Get("uploader"))
		if err != nil {
			return context
		}
		page, ok := contextPage(query.Get("page"))
		if !ok {
			return context
		}
		context.Source, context.Status, context.UploaderID, context.Page = "admin", status, uploaderID, page
	default:
		return context
	}
	return context
}

func contextPage(raw string) (int, bool) {
	if raw == "" {
		return 1, true
	}
	page, err := strconv.Atoi(raw)
	return page, err == nil && page > 0 && page <= 100000
}

func parseContextUploader(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0, strconv.ErrSyntax
	}
	return id, nil
}

func validPoolSlug(slug string) bool {
	if slug == "" || len(slug) > 120 {
		return false
	}
	for _, r := range slug {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func (c postContext) archive() archive.PostNavigationContext {
	return archive.PostNavigationContext{Source: c.Source, Query: c.Query, PoolSlug: c.PoolSlug, Status: c.Status, UploaderID: c.UploaderID}
}

func (c postContext) values() url.Values {
	values := url.Values{}
	values.Set("context", c.Source)
	switch c.Source {
	case "browse":
		if c.Query != "" {
			values.Set("q", c.Query)
		}
		if c.Page > 1 {
			values.Set("page", strconv.Itoa(c.Page))
		}
	case "pool":
		values.Set("pool", c.PoolSlug)
	case "uploads":
		values.Set("status", c.Status)
		if c.Page > 1 {
			values.Set("page", strconv.Itoa(c.Page))
		}
	case "admin":
		values.Set("status", c.Status)
		if c.UploaderID > 0 {
			values.Set("uploader", strconv.FormatInt(c.UploaderID, 10))
		}
		if c.Page > 1 {
			values.Set("page", strconv.Itoa(c.Page))
		}
	}
	return values
}

func (c postContext) detailPath(id int64) string {
	return "/posts/" + strconv.FormatInt(id, 10) + "?" + c.values().Encode()
}

func (c postContext) backPath() string {
	values := c.values()
	switch c.Source {
	case "browse":
		values.Del("context")
		return "/posts" + querySuffix(values)
	case "pool":
		return "/pools/" + c.PoolSlug
	case "uploads":
		values.Del("context")
		return "/uploads" + querySuffix(values)
	case "admin":
		values.Del("context")
		return "/admin/images" + querySuffix(values)
	default:
		return "/posts"
	}
}

func querySuffix(values url.Values) string {
	if encoded := values.Encode(); encoded != "" {
		return "?" + encoded
	}
	return ""
}

func postLink(id int64, context postContext) string { return context.detailPath(id) }

func (s *Server) postNavigation(r *http.Request, postID int64) (postContext, archive.PostNavigation, error) {
	context := parsePostContext(r)
	navigation, err := s.store.PostNeighbors(r.Context(), postID, viewerID(r), context.archive())
	if err == nil {
		return context, navigation, nil
	}
	if !errors.Is(err, archive.ErrNavigationUnavailable) {
		return postContext{}, archive.PostNavigation{}, err
	}
	if context.Source == "browse" && context.Query == "" && context.Page == 1 {
		return context, archive.PostNavigation{}, nil
	}
	fallback := postContext{Source: "browse", Page: 1}
	navigation, err = s.store.PostNeighbors(r.Context(), postID, viewerID(r), fallback.archive())
	if errors.Is(err, archive.ErrNavigationUnavailable) {
		return fallback, archive.PostNavigation{}, nil
	}
	return fallback, navigation, err
}
