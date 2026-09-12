package web

import (
	"fmt"
	"net/http"
)

func (s *Server) favorite(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	id, err := postID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err = s.store.SetFavorite(r.Context(), *user, id, r.FormValue("favorite") == "1"); err != nil {
		http.Error(w, "favorite could not be updated", http.StatusBadRequest)
		return
	}
	if isHTMX(r) {
		post, err := s.visiblePost(r)
		if err != nil {
			http.Error(w, "favorite could not be refreshed", http.StatusInternalServerError)
			return
		}
		s.render(w, r, "favorite-control", viewData{Post: post})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/posts/%d", id), http.StatusSeeOther)
}

func (s *Server) favorites(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	posts, err := s.store.Favorites(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "favorites unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "favorites", viewData{Title: "Favorites — Kura", ActiveNav: "favorites", Posts: posts})
}

func (s *Server) removeFavorite(w http.ResponseWriter, r *http.Request) {
	user := s.requireUser(w, r)
	if user == nil {
		return
	}
	id, err := postID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err = s.store.SetFavorite(r.Context(), *user, id, false); err != nil {
		http.Error(w, "favorite could not be removed", http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		posts, err := s.store.Favorites(r.Context(), user.ID)
		if err != nil {
			http.Error(w, "favorites could not be refreshed", http.StatusInternalServerError)
			return
		}
		if len(posts) == 0 {
			s.render(w, r, "favorites-empty", viewData{})
		}
		return
	}
	http.Redirect(w, r, "/favorites", http.StatusSeeOther)
}
