package web

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"kura/internal/archive"
)

func pageNumber(r *http.Request) int {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		return 1
	}
	return p
}

func groupTags(tags []archive.Tag) map[string][]archive.Tag {
	m := map[string][]archive.Tag{}
	for _, tag := range tags {
		m[tag.Category] = append(m[tag.Category], tag)
	}
	return m
}

func browseSort(r *http.Request) (string, error) {
	sort := r.URL.Query().Get("sort")
	if sort == "" {
		return archive.SearchSortNewest, nil
	}
	if sort != archive.SearchSortNewest && sort != archive.SearchSortOldest {
		return "", fmt.Errorf("%w: invalid sort", archive.ErrInvalidSearchQuery)
	}
	return sort, nil
}

func (s *Server) browseData(r *http.Request) (viewData, error) {
	q := r.URL.Query().Get("q")
	sort, err := browseSort(r)
	if err != nil {
		return viewData{}, err
	}
	p, err := s.store.ListPostsSorted(r.Context(), q, sort, pageNumber(r), 24)
	if err != nil {
		return viewData{}, err
	}
	tags, err := s.store.Tags(r.Context())
	if err != nil {
		return viewData{}, err
	}
	source := "/posts"
	if encoded := r.URL.Query().Encode(); encoded != "" {
		source += "?" + encoded
	}
	return viewData{Title: "Posts — Kura", ActiveNav: "posts", Page: p, Tags: tags, TagGroups: groupTags(tags), Query: p.Query, Sort: sort, BulkSource: source, PostContext: browsePostContext(r), ExportMaxPosts: archive.ExportMaxPosts, ExportMaxBytes: archive.ExportMaxBytes}, nil
}

func (s *Server) posts(w http.ResponseWriter, r *http.Request) {
	data, err := s.browseData(r)
	if err != nil {
		status := http.StatusInternalServerError
		message := ""
		if errors.Is(err, archive.ErrInvalidSearchQuery) {
			status, message = http.StatusBadRequest, "Search query or sort is invalid."
		}
		s.respondError(w, r, status, message)
		return
	}
	s.render(w, r, "posts", data)
}

func (s *Server) grid(w http.ResponseWriter, r *http.Request) {
	data, err := s.browseData(r)
	if err != nil {
		status := http.StatusInternalServerError
		message := ""
		if errors.Is(err, archive.ErrInvalidSearchQuery) {
			status, message = http.StatusBadRequest, "Search query or sort is invalid."
		}
		s.respondError(w, r, status, message)
		return
	}
	s.render(w, r, "browse-fragment", data)
}

func postID(r *http.Request) (int64, error) { return strconv.ParseInt(r.PathValue("id"), 10, 64) }

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

func (s *Server) visiblePost(r *http.Request) (archive.Post, error) {
	id, err := postID(r)
	if err != nil {
		return archive.Post{}, sql.ErrNoRows
	}
	user := currentUser(r)
	if user == nil {
		return s.store.Post(r.Context(), id)
	}
	return s.store.PostForUser(r.Context(), id, user.ID, user.CanUpload())
}

func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	p, err := s.visiblePost(r)
	if err == sql.ErrNoRows {
		s.respondError(w, r, http.StatusNotFound, "")
		return
	}
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	context, navigation, err := s.postNavigation(r, p.ID)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	var pools []archive.Pool
	if user := currentUser(r); user != nil && p.Status == "published" {
		pools, err = s.store.OwnedPoolsForPost(r.Context(), user.ID, p.ID)
		if err != nil {
			s.respondError(w, r, http.StatusInternalServerError, "")
			return
		}
	}
	data := viewData{Title: fmt.Sprintf("Post %d — Kura", p.ID), ActiveNav: "posts", Post: p, Pools: pools, BackURL: context.backPath()}
	if navigation.PreviousID > 0 {
		data.HasPrevious = true
		data.PreviousURL = context.detailPath(navigation.PreviousID)
	}
	if navigation.NextID > 0 {
		data.HasNext = true
		data.NextURL = context.detailPath(navigation.NextID)
	}
	s.render(w, r, "post", data)
}

func tagsString(tags []archive.Tag) string {
	values := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag.Category == "general" {
			values = append(values, tag.Name)
			continue
		}
		values = append(values, tag.Category+":"+tag.Name)
	}
	return strings.Join(values, " ")
}

func (s *Server) editPost(w http.ResponseWriter, r *http.Request) {
	if s.requireModerator(w, r) == nil {
		return
	}
	p, err := s.visiblePost(r)
	if err != nil {
		s.respondError(w, r, http.StatusNotFound, "")
		return
	}
	s.render(w, r, "post-edit", viewData{Title: fmt.Sprintf("Edit post %d — Kura", p.ID), ActiveNav: "posts", Post: p, PostTags: tagsString(p.Tags)})
}

func (s *Server) updatePost(w http.ResponseWriter, r *http.Request) {
	user := s.requireModerator(w, r)
	if user == nil {
		return
	}
	id, err := postID(r)
	if err != nil {
		s.respondError(w, r, http.StatusNotFound, "")
		return
	}
	if err = s.store.UpdatePost(r.Context(), *user, id, r.FormValue("source"), r.FormValue("tags"), r.FormValue("status")); err != nil {
		s.respondError(w, r, http.StatusBadRequest, "The post could not be updated.")
		return
	}
	if isHTMX(r) {
		post, err := s.visiblePost(r)
		if err != nil {
			s.respondError(w, r, http.StatusInternalServerError, "")
			return
		}
		s.render(w, r, "quick-edit", viewData{Post: post, PostTags: tagsString(post.Tags), Notice: "Saved."})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/posts/%d", id), http.StatusSeeOther)
}

func (s *Server) deletePost(w http.ResponseWriter, r *http.Request) {
	user := s.requireModerator(w, r)
	if user == nil {
		return
	}
	p, err := s.visiblePost(r)
	if err != nil {
		s.respondError(w, r, http.StatusNotFound, "")
		return
	}
	if user.Role == "admin" && p.UploaderID != user.ID {
		if err = s.store.QuarantinePost(r.Context(), *user, p.ID, r.FormValue("reason")); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, archive.ErrPermission) {
				status = http.StatusForbidden
			}
			s.respondError(w, r, status, "The post could not be quarantined.")
			return
		}
		http.Redirect(w, r, "/posts", http.StatusSeeOther)
		return
	}
	if _, err = s.store.PostForDeletion(r.Context(), *user, p.ID); err != nil {
		if errors.Is(err, archive.ErrPermission) {
			s.respondError(w, r, http.StatusForbidden, "You may delete only your own uploads.")
			return
		}
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	if strings.TrimSpace(r.FormValue("confirmation")) != "DELETE" {
		s.respondError(w, r, http.StatusBadRequest, "Type DELETE to permanently remove your upload.")
		return
	}
	if err = s.media.PermanentlyDelete(r.Context(), *user, p.ID); err != nil {
		if errors.Is(err, archive.ErrPermission) {
			s.respondError(w, r, http.StatusForbidden, "You may delete only your own uploads.")
			return
		}
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	http.Redirect(w, r, "/posts", http.StatusSeeOther)
}

func (s *Server) random(w http.ResponseWriter, r *http.Request) {
	id, err := s.store.RandomPostID(r.Context())
	if err == sql.ErrNoRows {
		http.Redirect(w, r, "/posts", http.StatusSeeOther)
		return
	}
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/posts/%d", id), http.StatusSeeOther)
}
