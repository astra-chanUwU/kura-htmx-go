package web

import (
	"errors"
	"net/http"
	"strconv"

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
	status := r.URL.Query().Get("status")
	if status != "draft" && status != "published" && status != "deleted" {
		status = "all"
	}
	page, err := s.store.ListPostsForAdmin(r.Context(), archive.AdminPostFilter{Status: status, UploaderID: uploaderID, Page: pageNumber(r), PerPage: 24})
	if err != nil {
		http.Error(w, "images unavailable", http.StatusInternalServerError)
		return
	}
	users, err := s.store.Users(r.Context())
	if err != nil {
		http.Error(w, "accounts unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "admin-images", viewData{Title: "Images — Kura", ActiveNav: "admin-images", Page: page, Status: status, Users: users, UploaderID: uploaderID})
}

func (s *Server) adminAccounts(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	users, err := s.store.Users(r.Context())
	if err != nil {
		http.Error(w, "accounts unavailable", http.StatusInternalServerError)
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
		http.Error(w, err.Error(), status)
		return
	}
	if isHTMX(r) {
		users, err := s.store.Users(r.Context())
		if err != nil {
			http.Error(w, "accounts unavailable", http.StatusInternalServerError)
			return
		}
		actor, err := s.store.User(r.Context(), currentUser(r).ID)
		if err != nil {
			http.Error(w, "account unavailable", http.StatusInternalServerError)
			return
		}
		s.render(w, r, "admin-account-list", viewData{Users: users, User: &actor})
		return
	}
	http.Redirect(w, r, "/admin/accounts", http.StatusSeeOther)
}
