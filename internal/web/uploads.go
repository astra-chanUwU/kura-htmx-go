package web

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"kura/internal/archive"
)

func uploadStatusFilter(r *http.Request) string {
	status := r.URL.Query().Get("status")
	if status != "draft" && status != "published" {
		return "all"
	}
	return status
}

func (s *Server) uploadsData(r *http.Request, actor archive.User) (viewData, error) {
	status := uploadStatusFilter(r)
	page, err := s.store.ListPostsForUploader(r.Context(), actor, archive.UploaderPostFilter{Status: status, Page: pageNumber(r), PerPage: 24})
	if err != nil {
		return viewData{}, err
	}
	return viewData{Title: "My uploads — Kura", ActiveNav: "uploads", Page: page, Status: status, PostContext: uploadsPostContext(status, page.Page)}, nil
}

func (s *Server) uploads(w http.ResponseWriter, r *http.Request) {
	actor := s.requireModerator(w, r)
	if actor == nil {
		return
	}
	data, err := s.uploadsData(r, *actor)
	if err != nil {
		if errors.Is(err, archive.ErrPermission) {
			s.respondError(w, r, http.StatusForbidden, "")
			return
		}
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	if isHTMX(r) {
		s.render(w, r, "uploads-list", data)
		return
	}
	s.render(w, r, "uploads", data)
}

func (s *Server) uploadMutationResponse(w http.ResponseWriter, r *http.Request) {
	if isHTMX(r) {
		actor := currentUser(r)
		if actor == nil {
			s.respondError(w, r, http.StatusForbidden, "")
			return
		}
		data, err := s.uploadsData(r, *actor)
		if err != nil {
			if errors.Is(err, archive.ErrPermission) {
				s.respondError(w, r, http.StatusForbidden, "")
				return
			}
			s.respondError(w, r, http.StatusInternalServerError, "")
			return
		}
		s.render(w, r, "uploads-list", data)
		return
	}
	status := uploadStatusFilter(r)
	page := pageNumber(r)
	http.Redirect(w, r, "/uploads?status="+url.QueryEscape(status)+"&page="+strconv.Itoa(page), http.StatusSeeOther)
}

func (s *Server) uploadStatus(w http.ResponseWriter, r *http.Request) {
	actor := s.requireModerator(w, r)
	if actor == nil {
		return
	}
	id, err := postID(r)
	if err != nil {
		s.respondError(w, r, http.StatusNotFound, "")
		return
	}
	status := r.FormValue("status")
	if status != "draft" && status != "published" {
		s.respondError(w, r, http.StatusBadRequest, "The upload status could not be changed.")
		return
	}
	if err = s.store.SetOwnedPostStatus(r.Context(), *actor, id, status); err != nil {
		s.uploadMutationError(w, r, err, "The upload status could not be changed.")
		return
	}
	s.uploadMutationResponse(w, r)
}

func (s *Server) deleteUpload(w http.ResponseWriter, r *http.Request) {
	actor := s.requireModerator(w, r)
	if actor == nil {
		return
	}
	id, err := postID(r)
	if err != nil {
		s.respondError(w, r, http.StatusNotFound, "")
		return
	}
	if err = s.store.SoftDeletePost(r.Context(), *actor, id); err != nil {
		s.uploadMutationError(w, r, err, "The upload could not be deleted.")
		return
	}
	s.uploadMutationResponse(w, r)
}

func (s *Server) uploadMutationError(w http.ResponseWriter, r *http.Request, err error, message string) {
	status := http.StatusInternalServerError
	if errors.Is(err, archive.ErrPermission) {
		status = http.StatusForbidden
	} else if errors.Is(err, sql.ErrNoRows) {
		status = http.StatusNotFound
	}
	s.respondError(w, r, status, message)
}
