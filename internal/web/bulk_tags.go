package web

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"kura/internal/archive"
)

func bulkPostIDs(r *http.Request) []int64 {
	var ids []int64
	for _, raw := range r.Form["post_ids"] {
		for _, value := range strings.Fields(raw) {
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				ids = append(ids, 0)
				continue
			}
			ids = append(ids, id)
		}
	}
	return ids
}

func bulkSource(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Path != "/posts" {
		return "/posts"
	}
	if parsed.RawQuery == "" {
		return "/posts"
	}
	return "/posts?" + parsed.RawQuery
}

func (s *Server) bulkTagFailure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, archive.ErrPermission):
		s.respondError(w, r, http.StatusForbidden, "")
	case errors.Is(err, sql.ErrNoRows):
		s.respondError(w, r, http.StatusBadRequest, "One or more selected posts are unavailable.")
	case errors.Is(err, archive.ErrBulkSelectionLimit), errors.Is(err, archive.ErrTagCategoryConflict):
		s.respondError(w, r, http.StatusBadRequest, "The bulk tag change could not be applied.")
	case errors.Is(err, archive.ErrAuditSnapshot):
		s.respondError(w, r, http.StatusBadRequest, "The bulk tag change could not be recorded safely.")
	default:
		s.respondError(w, r, http.StatusInternalServerError, "")
	}
}

func (s *Server) previewBulkTags(w http.ResponseWriter, r *http.Request) {
	user := s.requireModerator(w, r)
	if user == nil {
		return
	}
	addTags := r.FormValue("add_tags")
	removeTags := r.FormValue("remove_tags")
	preview, err := s.store.PreviewBulkTagDelta(r.Context(), *user, bulkPostIDs(r), addTags, removeTags)
	if err != nil {
		s.bulkTagFailure(w, r, err)
		return
	}
	s.render(w, r, "bulk-tags-preview", viewData{
		Title: "Preview bulk tags — Kura", ActiveNav: "posts", BulkPreview: preview,
		BulkAddTags: addTags, BulkRemoveTags: removeTags, BulkSource: bulkSource(r.FormValue("source")),
	})
}

func (s *Server) applyBulkTags(w http.ResponseWriter, r *http.Request) {
	user := s.requireModerator(w, r)
	if user == nil {
		return
	}
	if err := s.store.ApplyBulkTagDelta(r.Context(), *user, bulkPostIDs(r), r.FormValue("add_tags"), r.FormValue("remove_tags")); err != nil {
		s.bulkTagFailure(w, r, err)
		return
	}
	if isHTMX(r) {
		s.render(w, r, "bulk-tags-result", viewData{Notice: fmt.Sprintf("Bulk tags applied to %d posts.", len(uniqueBulkPostIDs(bulkPostIDs(r))))})
		return
	}
	http.Redirect(w, r, bulkSource(r.FormValue("source")), http.StatusSeeOther)
}

func uniqueBulkPostIDs(raw []int64) []int64 {
	seen := map[int64]bool{}
	ids := make([]int64, 0, len(raw))
	for _, id := range raw {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}
