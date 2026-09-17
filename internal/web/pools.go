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

func viewerID(r *http.Request) int64 {
	if user := currentUser(r); user != nil {
		return user.ID
	}
	return 0
}

func (s *Server) pools(w http.ResponseWriter, r *http.Request) {
	pools, err := s.store.PoolsForUser(r.Context(), viewerID(r))
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	s.render(w, r, "pools", viewData{Title: "Pools — Kura", ActiveNav: "pools", Pools: pools})
}

func (s *Server) pool(w http.ResponseWriter, r *http.Request) {
	pool, err := s.store.Pool(r.Context(), r.PathValue("slug"), viewerID(r))
	if err == sql.ErrNoRows {
		s.respondError(w, r, http.StatusNotFound, "")
		return
	}
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	var exportBytes int64
	for _, post := range pool.Posts {
		exportBytes += post.ByteSize
	}
	s.render(w, r, "pool", viewData{Title: pool.Name + " — Kura", ActiveNav: "pools", Pool: pool, PostContext: poolPostContext(pool.Slug), ExportBytes: exportBytes, ExportMaxPosts: archive.ExportMaxPosts, ExportMaxBytes: archive.ExportMaxBytes})
}

func (s *Server) newPool(w http.ResponseWriter, r *http.Request) {
	if s.requireUser(w, r) == nil {
		return
	}
	data, err := s.poolEditorData(r, archive.Pool{Status: "draft"}, "")
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	data.Title, data.ActiveNav = "New pool — Kura", "pools"
	s.render(w, r, "pool-edit", data)
}

func (s *Server) createPool(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	pool, err := s.store.CreatePool(r.Context(), *user, r.FormValue("name"), r.FormValue("description"), r.FormValue("status"), r.FormValue("post_ids"))
	if err != nil {
		data, loadErr := s.poolEditorData(r, archive.Pool{Name: r.FormValue("name"), Description: r.FormValue("description"), Status: r.FormValue("status")}, r.FormValue("post_ids"))
		if loadErr != nil {
			s.respondError(w, r, http.StatusInternalServerError, "")
			return
		}
		data.Title, data.ActiveNav, data.Error = "New pool — Kura", "pools", "The pool details could not be accepted."
		s.render(w, r, "pool-edit", data)
		return
	}
	http.Redirect(w, r, "/pools/"+pool.Slug, http.StatusSeeOther)
}

func (s *Server) editPool(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	pool, err := s.store.Pool(r.Context(), r.PathValue("slug"), user.ID)
	if err != nil || pool.OwnerID != user.ID {
		s.respondError(w, r, http.StatusForbidden, "This pool is not available for editing by this account.")
		return
	}
	data, err := s.poolEditorData(r, pool, pool.PostIDs())
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	data.Title, data.ActiveNav = "Edit "+pool.Name+" — Kura", "pools"
	s.render(w, r, "pool-edit", data)
}

func (s *Server) updatePool(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	err := s.store.UpdatePool(r.Context(), *user, r.PathValue("slug"), r.FormValue("name"), r.FormValue("description"), r.FormValue("status"), r.FormValue("post_ids"))
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, "The pool could not be updated.")
		return
	}
	http.Redirect(w, r, "/pools/"+r.PathValue("slug"), http.StatusSeeOther)
}

func selectedPostIDs(raw string) map[int64]bool {
	selected := map[int64]bool{}
	for _, value := range strings.Fields(raw) {
		id, err := strconv.ParseInt(value, 10, 64)
		if err == nil && id > 0 {
			selected[id] = true
		}
	}
	return selected
}

func (s *Server) poolEditorData(r *http.Request, pool archive.Pool, postIDs string) (viewData, error) {
	page, err := s.store.ListPosts(r.Context(), "", 1, 100)
	if err != nil {
		return viewData{}, err
	}
	pools, err := s.store.PoolsForUser(r.Context(), viewerID(r))
	if err != nil {
		return viewData{}, err
	}
	return viewData{Pool: pool, PoolPostIDs: postIDs, Page: page, Pools: pools, Selected: selectedPostIDs(postIDs), Source: "all"}, nil
}

func (s *Server) poolPicker(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	query := r.URL.Query()
	page, err := s.store.PoolCandidates(r.Context(), archive.PoolCandidateFilter{
		Source:   query.Get("source"),
		PoolSlug: query.Get("pool"),
		Query:    query.Get("q"),
		ViewerID: user.ID,
		Page:     1,
		PerPage:  100,
	})
	if err == sql.ErrNoRows {
		if query.Get("source") == "pool" && query.Get("pool") == "" {
			s.render(w, r, "pool-picker", viewData{Page: archive.PostPage{}, Selected: selectedPostIDs(query.Get("post_ids")), Source: "pool"})
			return
		}
		s.respondError(w, r, http.StatusNotFound, "")
		return
	}
	if err != nil {
		if errors.Is(err, archive.ErrInvalidSearchQuery) {
			s.respondError(w, r, http.StatusBadRequest, "Search query is invalid.")
			return
		}
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	s.render(w, r, "pool-picker", viewData{Page: page, Selected: selectedPostIDs(query.Get("post_ids")), Source: query.Get("source"), PoolSlug: query.Get("pool")})
}

func (s *Server) addPostToPool(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	id, err := postID(r)
	if err == nil {
		err = s.store.AddPostToPool(r.Context(), *user, r.FormValue("pool"), id)
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, archive.ErrPermission) {
			status = http.StatusForbidden
		}
		s.respondError(w, r, status, "The post could not be added to that pool.")
		return
	}
	if isHTMX(r) {
		post, err := s.visiblePost(r)
		if err != nil {
			s.respondError(w, r, http.StatusInternalServerError, "")
			return
		}
		pools, err := s.store.OwnedPoolsForPost(r.Context(), user.ID, id)
		if err != nil {
			s.respondError(w, r, http.StatusInternalServerError, "")
			return
		}
		s.render(w, r, "pool-control", viewData{Post: post, Pools: pools})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/posts/%d", id), http.StatusSeeOther)
}
