package web

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	mediafiles "kura/internal/media"
)

func (s *Server) newUpload(w http.ResponseWriter, r *http.Request) {
	if s.requireModerator(w, r) == nil {
		return
	}
	s.render(w, r, "upload", viewData{Title: "Upload — Kura", ActiveNav: "upload"})
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	user := s.requireModerator(w, r)
	if user == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, mediafiles.MaxUploadBytes+(2<<20))
	file, header, err := r.FormFile("image")
	if err != nil {
		s.render(w, r, "upload", viewData{Title: "Upload — Kura", ActiveNav: "upload", Error: "Choose an image to upload."})
		return
	}
	defer file.Close()
	post, err := s.media.Ingest(r.Context(), file, header, *user, r.FormValue("source"), r.FormValue("tags"), r.FormValue("status"))
	if err != nil {
		s.render(w, r, "upload", viewData{Title: "Upload — Kura", ActiveNav: "upload", Error: "The image could not be uploaded. Check the file and try again."})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/posts/%d", post.ID), http.StatusSeeOther)
}

func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != "originals" && kind != "thumbs" {
		writeProtocolError(w, http.StatusNotFound, "not found")
		return
	}
	rel := filepath.Clean(r.PathValue("path"))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		writeProtocolError(w, http.StatusNotFound, "not found")
		return
	}
	visible, err := s.store.MediaVisibleToUser(r.Context(), kind, filepath.ToSlash(filepath.Join(kind, rel)), viewerID(r), currentUser(r) != nil && currentUser(r).CanUpload())
	if err != nil {
		writeProtocolError(w, http.StatusInternalServerError, "media unavailable")
		return
	}
	if !visible {
		writeProtocolError(w, http.StatusNotFound, "not found")
		return
	}
	root, err := filepath.Abs(s.mediaRoot)
	if err != nil {
		writeProtocolError(w, http.StatusNotFound, "not found")
		return
	}
	path := filepath.Join(root, kind, rel)
	if _, err = os.Stat(path); err != nil {
		writeProtocolError(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeFile(w, r, path)
}
