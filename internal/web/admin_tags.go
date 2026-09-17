package web

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"kura/internal/archive"
)

func tagInventoryFilter(r *http.Request) archive.TagInventoryFilter {
	return archive.TagInventoryFilter{Query: r.URL.Query().Get("q"), Category: r.URL.Query().Get("category"), Page: pageNumber(r), PerPage: 24}
}

func (s *Server) adminTags(w http.ResponseWriter, r *http.Request) {
	actor := s.requireSuperAdmin(w, r)
	if actor == nil {
		return
	}
	filter := tagInventoryFilter(r)
	page, err := s.store.ListTagInventory(r.Context(), *actor, filter)
	if err != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	s.render(w, r, "admin-tags", viewData{Title: "Tag maintenance — Kura", ActiveNav: "admin-tags", TagPage: page})
}

func tagMaintenanceRequest(r *http.Request) archive.TagMaintenanceRequest {
	return archive.TagMaintenanceRequest{Operation: strings.TrimSpace(r.FormValue("operation")), Source: strings.TrimSpace(r.FormValue("source")), Target: strings.TrimSpace(r.FormValue("target")), Reason: strings.TrimSpace(r.FormValue("reason"))}
}

func (s *Server) adminTagsPreview(w http.ResponseWriter, r *http.Request) {
	actor := s.requireSuperAdmin(w, r)
	if actor == nil {
		return
	}
	request := tagMaintenanceRequest(r)
	preview, err := s.store.PreviewTagMaintenance(r.Context(), *actor, request)
	filter := archive.TagInventoryFilter{Query: r.FormValue("query"), Category: r.FormValue("category"), Page: 1, PerPage: 24}
	page, pageErr := s.store.ListTagInventory(r.Context(), *actor, filter)
	if pageErr != nil {
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	data := viewData{Title: "Tag maintenance — Kura", ActiveNav: "admin-tags", TagPage: page, TagPreview: preview}
	if err != nil {
		data.Error = tagMaintenanceMessage(err)
	}
	s.render(w, r, "admin-tags", data)
}

func (s *Server) adminTagsApply(w http.ResponseWriter, r *http.Request) {
	actor := s.requireSuperAdmin(w, r)
	if actor == nil {
		return
	}
	request := tagMaintenanceRequest(r)
	expected := archive.TagMaintenancePreview{Operation: request.Operation, Fingerprint: strings.TrimSpace(r.FormValue("expected_fingerprint")), Source: archive.Tag{Name: strings.TrimSpace(r.FormValue("source"))}, Target: archive.Tag{Name: strings.TrimSpace(r.FormValue("target"))}}
	err := s.store.ApplyTagMaintenance(r.Context(), *actor, request, expected)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, archive.ErrPermission) {
			status = http.StatusForbidden
		} else if errors.Is(err, archive.ErrTagMaintenanceStale) || errors.Is(err, archive.ErrTagMaintenanceConflict) || errors.Is(err, archive.ErrTagMaintenanceCategoryConflict) {
			status = http.StatusConflict
		} else if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		s.respondError(w, r, status, tagMaintenanceMessage(err))
		return
	}
	if isHTMX(r) {
		s.adminTags(w, r)
		return
	}
	query := url.Values{}
	if value := r.FormValue("query"); value != "" {
		query.Set("q", value)
	}
	if value := r.FormValue("category"); value != "" {
		query.Set("category", value)
	}
	location := "/admin/tags"
	if encoded := query.Encode(); encoded != "" {
		location += "?" + encoded
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func tagMaintenanceMessage(err error) string {
	switch {
	case errors.Is(err, archive.ErrTagMaintenanceStale):
		return "This preview is stale because the tag state changed. Preview the operation again; nothing was overwritten."
	case errors.Is(err, archive.ErrTagMaintenanceCategoryConflict):
		return "The merge is not allowed because the source and target categories differ."
	case errors.Is(err, archive.ErrTagMaintenanceConflict):
		return "The requested tag change conflicts with the current tag inventory."
	case errors.Is(err, archive.ErrTagMaintenanceNoOp):
		return "The requested tag change would make no change."
	case errors.Is(err, sql.ErrNoRows):
		return "The source or target tag no longer exists."
	default:
		return "The tag change could not be applied."
	}
}
