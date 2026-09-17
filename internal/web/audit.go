package web

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"kura/internal/archive"
)

func auditAction(r *http.Request) string {
	action := r.URL.Query().Get("action")
	for _, allowed := range archive.AuditEventTypes() {
		if action == allowed {
			return action
		}
	}
	return ""
}

func auditFilter(r *http.Request) archive.AuditFilter {
	actorID, _ := strconv.ParseInt(r.URL.Query().Get("actor"), 10, 64)
	postID, _ := strconv.ParseInt(r.URL.Query().Get("post"), 10, 64)
	if actorID < 1 {
		actorID = 0
	}
	if postID < 1 {
		postID = 0
	}
	return archive.AuditFilter{EventType: auditAction(r), ActorID: actorID, PostID: postID, Page: pageNumber(r), PerPage: 24}
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	actor := s.requireSuperAdmin(w, r)
	if actor == nil {
		return
	}
	filter := auditFilter(r)
	page, err := s.store.ListAuditEvents(r.Context(), *actor, filter)
	if err != nil {
		if errors.Is(err, archive.ErrPermission) {
			s.respondError(w, r, http.StatusForbidden, "")
			return
		}
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	users, err := s.store.Users(r.Context())
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	s.render(w, r, "admin-audit", viewData{Title: "Audit log — Kura", ActiveNav: "admin-audit", Audit: page, AuditActions: archive.AuditEventTypes(), Users: users})
}

func (s *Server) adminAuditRevert(w http.ResponseWriter, r *http.Request) {
	actor := s.requireSuperAdmin(w, r)
	if actor == nil {
		return
	}
	id, err := targetID(r)
	if err == nil {
		err = s.store.RevertAuditEvent(r.Context(), *actor, id)
	}
	if err != nil {
		status := http.StatusBadRequest
		message := "The audit event could not be reverted."
		switch {
		case errors.Is(err, archive.ErrPermission):
			status = http.StatusForbidden
		case errors.Is(err, sql.ErrNoRows):
			status = http.StatusNotFound
		case errors.Is(err, archive.ErrAuditConflict):
			status = http.StatusConflict
			message = "This audit event is stale because the post changed afterward. Nothing was overwritten."
		case errors.Is(err, archive.ErrAuditNotRevertable):
			status = http.StatusConflict
			message = "This audit event cannot be reverted because the post is unavailable or the event is informational."
		}
		s.respondError(w, r, status, message)
		return
	}
	if isHTMX(r) {
		s.adminAudit(w, r)
		return
	}
	http.Redirect(w, r, "/admin/audit", http.StatusSeeOther)
}

func auditEventLabel(event archive.AuditEvent) string {
	if event.EventType == "revert" && event.RevertedEventID > 0 {
		return fmt.Sprintf("revert of #%d", event.RevertedEventID)
	}
	return auditActionLabel(event.EventType)
}

func auditActionLabel(eventType string) string {
	switch eventType {
	case "metadata_change":
		return "Metadata change"
	case "bulk_tag_change":
		return "Bulk tag change"
	case "tag_rename":
		return "Tag rename"
	case "tag_merge":
		return "Tag merge"
	case "quarantine":
		return "Quarantine"
	case "restore":
		return "Restore"
	case "role_change":
		return "Role change"
	case "role_change_quarantine":
		return "Draft quarantine during role change"
	case "suspension_change":
		return "Account suspension change"
	case "super_admin_transfer":
		return "Super-admin transfer"
	case "permanent_delete":
		return "Permanent deletion"
	case "invite_created":
		return "Invitation created"
	case "invite_revoked":
		return "Invitation revoked"
	case "invite_consumed":
		return "Invitation used"
	case "revert":
		return "Revert"
	default:
		return eventType
	}
}
