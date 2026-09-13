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
			http.Error(w, "moderator access required", http.StatusForbidden)
			return
		}
		http.Error(w, "tag suggestions unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "tag-suggestions", viewData{Tags: tags})
}
