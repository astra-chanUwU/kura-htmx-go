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

func (s *Server) browseData(r *http.Request) (viewData, error) {
	q := r.URL.Query().Get("q")
	p, err := s.store.ListPosts(r.Context(), q, pageNumber(r), 24)
	if err != nil {
		return viewData{}, err
	}
	tags, err := s.store.Tags(r.Context())
	if err != nil {
		return viewData{}, err
	}
	return viewData{Title: "Posts — Kura", ActiveNav: "posts", Page: p, Tags: tags, TagGroups: groupTags(tags), Query: p.Query}, nil
}

func (s *Server) posts(w http.ResponseWriter, r *http.Request) {
	data, err := s.browseData(r)
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "posts", data)
}

func (s *Server) grid(w http.ResponseWriter, r *http.Request) {
	data, err := s.browseData(r)
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
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
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	var pools []archive.Pool
	if user := currentUser(r); user != nil && p.Status == "published" {
		pools, err = s.store.OwnedPoolsForPost(r.Context(), user.ID, p.ID)
		if err != nil {
			http.Error(w, "pools unavailable", http.StatusInternalServerError)
			return
		}
	}
	s.render(w, r, "post", viewData{Title: fmt.Sprintf("Post %d — Kura", p.ID), ActiveNav: "posts", Post: p, Pools: pools})
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
		http.NotFound(w, r)
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
		http.NotFound(w, r)
		return
	}
	if err = s.store.UpdatePost(r.Context(), *user, id, r.FormValue("source"), r.FormValue("tags"), r.FormValue("status")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if isHTMX(r) {
		post, err := s.visiblePost(r)
		if err != nil {
			http.Error(w, "post could not be refreshed", http.StatusInternalServerError)
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
		http.NotFound(w, r)
		return
	}
	if err = s.store.SoftDeletePost(r.Context(), *user, p.ID); err != nil {
		if errors.Is(err, archive.ErrPermission) {
			http.Error(w, "you may delete only your own uploads", http.StatusForbidden)
			return
		}
		http.Error(w, "post could not be deleted", http.StatusInternalServerError)
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
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/posts/%d", id), http.StatusSeeOther)
}
