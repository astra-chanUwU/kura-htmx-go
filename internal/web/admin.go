package web

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"kura/internal/archive"
)

func (s *Server) adminImages(w http.ResponseWriter, r *http.Request) {
	if s.requireSuperAdmin(w, r) == nil {
		return
	}
	uploaderID, _ := strconv.ParseInt(r.URL.Query().Get("uploader"), 10, 64)
	if uploaderID < 1 {
		uploaderID = 0
	}
	status := adminImageStatus(r)
	page, err := s.store.ListPostsForAdmin(r.Context(), archive.AdminPostFilter{Status: status, UploaderID: uploaderID, Page: pageNumber(r), PerPage: 24})
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	users, err := s.store.Users(r.Context())
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	s.render(w, r, "admin-images", viewData{Title: "Images — Kura", ActiveNav: "admin-images", Page: page, Status: status, Users: users, UploaderID: uploaderID, PostContext: adminPostContext(status, uploaderID, page.Page)})
}

func adminImageStatus(r *http.Request) string {
	status := r.URL.Query().Get("status")
	if status != "draft" && status != "published" && status != "deleted" && status != "quarantined" {
		return "all"
	}
	return status
}

func (s *Server) adminImageReview(w http.ResponseWriter, r *http.Request) {
	actor := s.requireSuperAdmin(w, r)
	if actor == nil {
		return
	}
	id, err := targetID(r)
	if err != nil {
		s.respondError(w, r, http.StatusNotFound, "")
		return
	}
	post, err := s.store.PostForReview(r.Context(), *actor, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.respondError(w, r, http.StatusNotFound, "")
			return
		}
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	s.render(w, r, "admin-review", viewData{Title: "Review post " + strconv.FormatInt(id, 10) + " — Kura", ActiveNav: "admin-images", Post: post})
}

func (s *Server) adminQuarantine(w http.ResponseWriter, r *http.Request) {
	actor := s.requireSuperAdmin(w, r)
	if actor == nil {
		return
	}
	id, err := targetID(r)
	if err == nil {
		err = s.store.QuarantinePost(r.Context(), *actor, id, r.FormValue("reason"))
	}
	s.adminImageMutationResult(w, r, err)
}

func (s *Server) adminRestore(w http.ResponseWriter, r *http.Request) {
	actor := s.requireSuperAdmin(w, r)
	if actor == nil {
		return
	}
	id, err := targetID(r)
	if err == nil {
		err = s.store.RestorePost(r.Context(), *actor, id)
	}
	s.adminImageMutationResult(w, r, err)
}

func (s *Server) adminPermanentDelete(w http.ResponseWriter, r *http.Request) {
	actor := s.requireSuperAdmin(w, r)
	if actor == nil {
		return
	}
	id, err := targetID(r)
	if err == nil {
		_, postErr := s.store.PostForDeletion(r.Context(), *actor, id)
		if postErr != nil {
			err = postErr
		} else if strings.TrimSpace(r.FormValue("confirmation")) != strconv.FormatInt(id, 10) {
			err = errors.New("post ID confirmation is required")
		} else {
			err = s.media.PermanentlyDelete(r.Context(), *actor, id)
		}
	}
	s.adminImageMutationResult(w, r, err)
}

func (s *Server) adminImageMutationResult(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, archive.ErrPermission) {
			status = http.StatusForbidden
		} else if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		s.respondError(w, r, status, "The image change could not be applied.")
		return
	}
	if isHTMX(r) {
		s.adminImages(w, r)
		return
	}
	http.Redirect(w, r, "/admin/images?status="+adminImageStatus(r), http.StatusSeeOther)
}

func (s *Server) adminAccounts(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	users, err := s.store.Users(r.Context())
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	s.render(w, r, "admin", viewData{Title: "Accounts — Kura", ActiveNav: "admin", Users: users})
}

func targetID(r *http.Request) (int64, error) { return strconv.ParseInt(r.PathValue("id"), 10, 64) }

func (s *Server) adminRole(w http.ResponseWriter, r *http.Request) {
	actor := s.requireAdmin(w, r)
	if actor == nil {
		return
	}
	id, err := targetID(r)
	if err == nil {
		err = s.store.SetUserRole(r.Context(), *actor, id, r.FormValue("role"))
	}
	s.adminResult(w, r, err)
}

func (s *Server) adminSuspension(w http.ResponseWriter, r *http.Request) {
	actor := s.requireAdmin(w, r)
	if actor == nil {
		return
	}
	id, err := targetID(r)
	if err == nil {
		err = s.store.SetUserSuspended(r.Context(), *actor, id, r.FormValue("suspended") == "1")
	}
	s.adminResult(w, r, err)
}

func (s *Server) adminTransfer(w http.ResponseWriter, r *http.Request) {
	actor := s.requireAdmin(w, r)
	if actor == nil {
		return
	}
	id, err := targetID(r)
	if err == nil {
		err = s.store.TransferSuperAdmin(r.Context(), *actor, id)
	}
	s.adminResult(w, r, err)
}

func (s *Server) adminResult(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, archive.ErrPermission) {
			status = http.StatusForbidden
		}
		s.respondError(w, r, status, "The account change could not be applied.")
		return
	}
	if isHTMX(r) {
		users, err := s.store.Users(r.Context())
		if err != nil {
			s.respondError(w, r, http.StatusInternalServerError, "")
			return
		}
		actor, err := s.store.User(r.Context(), currentUser(r).ID)
		if err != nil {
			s.respondError(w, r, http.StatusInternalServerError, "")
			return
		}
		s.render(w, r, "admin-account-list", viewData{Users: users, User: &actor})
		return
	}
	http.Redirect(w, r, "/admin/accounts", http.StatusSeeOther)
}
