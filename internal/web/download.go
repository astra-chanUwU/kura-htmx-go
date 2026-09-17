package web

import (
	"database/sql"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"kura/internal/archive"
)

func downloadExtension(post archive.Post, mode string) string {
	if mode == "original" && post.OriginalFilename != "" {
		ext := strings.ToLower(filepath.Ext(strings.ReplaceAll(post.OriginalFilename, "\\", "/")))
		switch post.MIMEType {
		case "image/jpeg":
			if ext == ".jpg" || ext == ".jpeg" {
				return ext
			}
		case "image/png":
			if ext == ".png" {
				return ext
			}
		case "image/gif":
			if ext == ".gif" {
				return ext
			}
		}
	}
	switch post.MIMEType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	default:
		return filepath.Ext(post.OriginalPath)
	}
}

func safeDownloadStem(raw string) string {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\\", "/"), "/", "_")
	var b strings.Builder
	for _, r := range raw {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-' || r == '.':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		default:
			b.WriteByte('_')
		}
	}
	stem := strings.Trim(strings.Join(strings.Fields(b.String()), " "), " .")
	if stem == "" || stem == "." || stem == ".." {
		return "image"
	}
	if runes := []rune(stem); len(runes) > 160 {
		stem = strings.TrimRight(string(runes[:160]), " .")
	}
	if stem == "" {
		return "image"
	}
	return stem
}

func originalStem(filename string) string {
	filename = strings.ReplaceAll(filename, "\\", "/")
	base := filepath.Base(filename)
	return safeDownloadStem(strings.TrimSuffix(base, filepath.Ext(base)))
}

func downloadFilename(post archive.Post, mode string) string {
	ext := downloadExtension(post, mode)
	if ext == "" {
		ext = ".bin"
	}
	stem := "image"
	if mode == "original" && post.OriginalFilename != "" {
		stem = originalStem(post.OriginalFilename)
	} else if mode == "tagged" {
		parts := make([]string, 0, len(post.Tags))
		for _, tag := range post.Tags {
			parts = append(parts, safeDownloadStem(tag.Name))
		}
		if len(parts) > 0 {
			stem = strings.Join(parts, " ")
		}
	}
	return fmt.Sprintf("kura-%d__%s%s", post.ID, stem, ext)
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	id, err := postID(r)
	if err != nil {
		writeProtocolError(w, http.StatusNotFound, "not found")
		return
	}
	var post archive.Post
	if user := currentUser(r); user == nil {
		post, err = s.store.Post(r.Context(), id)
	} else {
		post, err = s.store.PostForUser(r.Context(), id, user.ID, user.CanUpload())
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeProtocolError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeProtocolError(w, http.StatusInternalServerError, "download unavailable")
		return
	}
	rel := filepath.FromSlash(post.OriginalPath)
	if filepath.IsAbs(rel) || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		writeProtocolError(w, http.StatusNotFound, "not found")
		return
	}
	root, err := filepath.Abs(s.mediaRoot)
	if err != nil {
		writeProtocolError(w, http.StatusNotFound, "not found")
		return
	}
	path := filepath.Join(root, rel)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		writeProtocolError(w, http.StatusNotFound, "not found")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeProtocolError(w, http.StatusNotFound, "not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeProtocolError(w, http.StatusNotFound, "not found")
		return
	}
	mode := r.URL.Query().Get("name")
	if mode != "original" {
		mode = "tagged"
	}
	filename := downloadFilename(post, mode)
	w.Header().Set("Content-Type", post.MIMEType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, filename, info.ModTime(), file)
}
