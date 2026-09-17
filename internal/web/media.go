package web

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	mediafiles "kura/internal/media"
)

type uploadResult struct {
	Filename string
	Status   string
	Message  string
	PostID   int64
}

func (s *Server) newUpload(w http.ResponseWriter, r *http.Request) {
	if s.requireModerator(w, r) == nil {
		return
	}
	s.render(w, r, "upload", viewData{Title: "Upload — Kura", ActiveNav: "upload"})
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	user := s.requireModerator(w, r)
	if user == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, mediafiles.MaxUploadRequestBytes)
	if r.MultipartForm == nil {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			s.render(w, r, "upload", viewData{Title: "Upload — Kura", ActiveNav: "upload", Error: "Choose one or more images to upload."})
			return
		}
	}
	headerList := r.MultipartForm.File["image"]
	if len(headerList) == 0 {
		s.render(w, r, "upload", viewData{Title: "Upload — Kura", ActiveNav: "upload", Error: "Choose an image to upload."})
		return
	}
	if len(headerList) > mediafiles.MaxUploadCount {
		s.render(w, r, "upload", viewData{Title: "Upload — Kura", ActiveNav: "upload", Error: fmt.Sprintf("Choose at most %d images at a time.", mediafiles.MaxUploadCount)})
		return
	}
	status := r.FormValue("status")
	if status == "" {
		status = "published"
	}
	if len(headerList) > 1 {
		status = "draft"
		if r.FormValue("publish_batch") == "1" {
			status = "published"
		}
	}
	if len(headerList) == 1 {
		file, err := headerList[0].Open()
		if err != nil {
			s.render(w, r, "upload", viewData{Title: "Upload — Kura", ActiveNav: "upload", Error: "The image could not be uploaded. Check the file and try again."})
			return
		}
		post, ingestErr := s.media.Ingest(r.Context(), file, headerList[0], *user, r.FormValue("source"), r.FormValue("tags"), status)
		_ = file.Close()
		if ingestErr != nil {
			s.render(w, r, "upload", viewData{Title: "Upload — Kura", ActiveNav: "upload", Error: "The image could not be uploaded. Check the file and try again."})
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/posts/%d", post.ID), http.StatusSeeOther)
		return
	}
	results := make([]uploadResult, 0, len(headerList))
	for _, header := range headerList {
		result := uploadResult{Filename: header.Filename}
		file, err := header.Open()
		if err != nil {
			result.Status = "failed"
			result.Message = "could not be uploaded"
			results = append(results, result)
			continue
		}
		post, ingestErr := s.media.Ingest(r.Context(), file, header, *user, r.FormValue("source"), r.FormValue("tags"), status)
		_ = file.Close()
		if ingestErr == nil {
			result.Status = post.Status
			result.PostID = post.ID
			result.Message = "created"
		} else if errors.Is(ingestErr, mediafiles.ErrDuplicate) {
			result.Status = "duplicate"
			result.Message = "duplicate — not created"
		} else {
			result.Status = "validation"
			result.Message = "validation failure — not created"
		}
		results = append(results, result)
	}
	s.render(w, r, "upload", viewData{Title: "Upload — Kura", ActiveNav: "upload", BatchResults: results})
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
