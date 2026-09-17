package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"kura/internal/archive"
)

func (s *Server) adminInvites(w http.ResponseWriter, r *http.Request) {
	actor := s.requireSuperAdmin(w, r)
	if actor == nil {
		return
	}
	invites, err := s.store.RegistrationInvites(r.Context(), *actor)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	s.render(w, r, "admin-invites", viewData{Title: "Registration invitations — Kura", ActiveNav: "admin-invites", Invites: invites, InviteToken: r.URL.Query().Get("created")})
}

func (s *Server) createAdminInvite(w http.ResponseWriter, r *http.Request) {
	actor := s.requireSuperAdmin(w, r)
	if actor == nil {
		return
	}
	minutes := 24 * 60
	if raw := r.FormValue("expires_minutes"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			s.respondError(w, r, http.StatusBadRequest, "Invitation expiry must be a positive number of minutes.")
			return
		}
		minutes = parsed
	}
	invite, err := s.store.CreateRegistrationInvite(r.Context(), *actor, time.Duration(minutes)*time.Minute)
	if err != nil {
		if errors.Is(err, archive.ErrPermission) {
			s.respondError(w, r, http.StatusForbidden, "")
			return
		}
		s.respondError(w, r, http.StatusBadRequest, "The invitation could not be created.")
		return
	}
	s.renderAdminInvites(w, r, invite.Token)
}

func (s *Server) revokeAdminInvite(w http.ResponseWriter, r *http.Request) {
	actor := s.requireSuperAdmin(w, r)
	if actor == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err == nil {
		err = s.store.RevokeRegistrationInvite(r.Context(), *actor, id)
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, archive.ErrPermission) {
			status = http.StatusForbidden
		}
		s.respondError(w, r, status, "The invitation could not be revoked.")
		return
	}
	if isHTMX(r) {
		s.adminInvites(w, r)
		return
	}
	http.Redirect(w, r, "/admin/invites", http.StatusSeeOther)
}

func (s *Server) renderAdminInvites(w http.ResponseWriter, r *http.Request, token string) {
	actor := currentUser(r)
	if actor == nil {
		s.respondError(w, r, http.StatusForbidden, "")
		return
	}
	invites, err := s.store.RegistrationInvites(r.Context(), *actor)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	s.render(w, r, "admin-invites", viewData{Title: "Registration invitations — Kura", ActiveNav: "admin-invites", Invites: invites, InviteToken: token, InviteURL: inviteURL(token)})
}
