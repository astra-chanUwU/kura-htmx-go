package web

import (
	"errors"
	"net/http"

	"kura/internal/archive"
)

func (s *Server) tagSuggestions(w http.ResponseWriter, r *http.Request) {
	user := s.requireModerator(w, r)
	if user == nil {
		return
	}
	tags, err := s.store.TagSuggestions(r.Context(), *user, r.URL.Query().Get("q"))
	if err != nil {
		if errors.Is(err, archive.ErrPermission) {
			s.respondError(w, r, http.StatusForbidden, "")
			return
		}
		s.respondError(w, r, http.StatusInternalServerError, "")
		return
	}
	s.render(w, r, "tag-suggestions", viewData{Tags: tags})
}
