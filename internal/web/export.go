package web

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"kura/internal/archive"
)

var (
	errExportOriginalMissing = errors.New("export original is missing")
	errExportOriginalChanged = errors.New("export original changed")
)

const maxExportFilenameRunes = 220

type exportManifest struct {
	SchemaVersion int                  `json:"schema_version"`
	ExportedAt    string               `json:"exported_at"`
	Source        exportManifestSource `json:"source"`
	Posts         []exportManifestPost `json:"posts"`
}

type exportManifestSource struct {
	Type   string `json:"type"`
	Slug   string `json:"slug,omitempty"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
}

type exportManifestPost struct {
	Filename         string              `json:"filename"`
	PostID           int64               `json:"post_id"`
	Tags             []exportManifestTag `json:"tags"`
	Source           string              `json:"source"`
	SHA256           string              `json:"sha256"`
	OriginalBasename string              `json:"original_basename,omitempty"`
	MIMEType         string              `json:"mime_type"`
	Width            int                 `json:"width"`
	Height           int                 `json:"height"`
	ByteSize         int64               `json:"byte_size"`
}

type exportManifestTag struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Category    string `json:"category"`
}

type exportContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r exportContextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.r.Read(p)
	}
}

func parseExportIDs(values []string) ([]int64, error) {
	if len(values) == 0 {
		return nil, archive.ErrExportInvalidSelection
	}
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id < 1 {
			return nil, archive.ErrExportInvalidSelection
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func exportMode(r *http.Request) (string, error) {
	mode := r.URL.Query().Get("name")
	if mode == "" {
		return "tagged", nil
	}
	if mode != "tagged" && mode != "original" {
		return "", archive.ErrExportInvalidSelection
	}
	return mode, nil
}

func exportArchiveFilename(plan archive.ExportPlan) string {
	if plan.Source.Type == "pool" {
		return "kura-" + safeDownloadStem(plan.Source.Slug) + ".zip"
	}
	return "kura-selected-images.zip"
}

func exportMemberFilename(post archive.Post, mode string) string {
	filename := downloadFilename(post, mode)
	runes := []rune(filename)
	if len(runes) <= maxExportFilenameRunes {
		return filename
	}
	ext := filepath.Ext(filename)
	stem := strings.TrimSuffix(filename, ext)
	available := maxExportFilenameRunes - len([]rune(ext))
	if available < 1 {
		return string(runes[:maxExportFilenameRunes])
	}
	stemRunes := []rune(stem)
	if len(stemRunes) > available {
		stem = string(stemRunes[:available])
	}
	return strings.TrimRight(stem, " .") + ext
}

func exportOriginalBasename(filename string) string {
	filename = strings.TrimSpace(strings.ReplaceAll(filename, "\\", "/"))
	if filename == "" {
		return ""
	}
	base := filepath.Base(filename)
	if base == "." || base == ".." || base == "" {
		return ""
	}
	return base
}

func exportManifestFor(plan archive.ExportPlan, mode string) exportManifest {
	manifest := exportManifest{
		SchemaVersion: 1,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339),
		Source: exportManifestSource{
			Type: plan.Source.Type, Slug: plan.Source.Slug, Name: plan.Source.Name, Status: plan.Source.Status,
		},
		Posts: make([]exportManifestPost, 0, len(plan.Posts)),
	}
	for _, post := range plan.Posts {
		record := exportManifestPost{
			Filename:         exportMemberFilename(post, mode),
			PostID:           post.ID,
			Source:           post.Source,
			SHA256:           post.SHA256,
			OriginalBasename: exportOriginalBasename(post.OriginalFilename),
			MIMEType:         post.MIMEType,
			Width:            post.Width,
			Height:           post.Height,
			ByteSize:         post.ByteSize,
			Tags:             make([]exportManifestTag, 0, len(post.Tags)),
		}
		for _, tag := range post.Tags {
			record.Tags = append(record.Tags, exportManifestTag{Name: tag.Name, DisplayName: tag.DisplayName, Category: tag.Category})
		}
		manifest.Posts = append(manifest.Posts, record)
	}
	return manifest
}

func (s *Server) exportOriginalFile(post archive.Post) (*os.File, os.FileInfo, error) {
	rel := filepath.FromSlash(post.OriginalPath)
	if filepath.IsAbs(rel) || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, nil, errExportOriginalMissing
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == ".." {
			return nil, nil, errExportOriginalMissing
		}
	}
	root, err := filepath.Abs(s.mediaRoot)
	if err != nil {
		return nil, nil, errExportOriginalMissing
	}
	path := filepath.Join(root, rel)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, nil, errExportOriginalMissing
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, errExportOriginalMissing
	}
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, errExportOriginalMissing
	}
	if info.Size() != post.ByteSize {
		file.Close()
		return nil, nil, errExportOriginalChanged
	}
	return file, info, nil
}

func (s *Server) stageExport(ctx context.Context, plan archive.ExportPlan, mode string) (path string, err error) {
	temp, err := os.CreateTemp("", "kura-export-*.zip")
	if err != nil {
		return "", err
	}
	path = temp.Name()
	defer func() {
		if temp != nil {
			_ = temp.Close()
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()

	zipWriter := zip.NewWriter(temp)
	manifestBytes, err := json.MarshalIndent(exportManifestFor(plan, mode), "", "  ")
	if err != nil {
		return "", err
	}
	manifestBytes = append(manifestBytes, '\n')
	manifestHeader := &zip.FileHeader{Name: "manifest.json", Method: zip.Deflate}
	manifestFile, err := zipWriter.CreateHeader(manifestHeader)
	if err != nil {
		return "", err
	}
	if _, err = manifestFile.Write(manifestBytes); err != nil {
		return "", err
	}

	for _, post := range plan.Posts {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		file, info, openErr := s.exportOriginalFile(post)
		if openErr != nil {
			return "", openErr
		}
		entry, createErr := zipWriter.CreateHeader(&zip.FileHeader{Name: exportMemberFilename(post, mode), Method: zip.Store})
		if createErr != nil {
			file.Close()
			return "", createErr
		}
		hasher := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(entry, hasher), exportContextReader{ctx: ctx, r: file})
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if written != info.Size() || written != post.ByteSize || !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), post.SHA256) {
			return "", errExportOriginalChanged
		}
	}
	if err = zipWriter.Close(); err != nil {
		return "", err
	}
	if err = temp.Close(); err != nil {
		return "", err
	}
	temp = nil
	return path, nil
}

func exportError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "export unavailable"
	switch {
	case errors.Is(err, sql.ErrNoRows):
		status, message = http.StatusNotFound, "export source not found"
	case errors.Is(err, archive.ErrExportInvalidSelection), errors.Is(err, archive.ErrExportUnsupportedMIME):
		status, message = http.StatusBadRequest, "the export selection is invalid"
	case errors.Is(err, archive.ErrExportSelectionLimit), errors.Is(err, archive.ErrExportBytesLimit):
		status, message = http.StatusRequestEntityTooLarge, "the export exceeds its limits"
	case errors.Is(err, archive.ErrExportUnavailable), errors.Is(err, errExportOriginalChanged):
		status, message = http.StatusConflict, "some export images are no longer available"
	case errors.Is(err, errExportOriginalMissing):
		status, message = http.StatusNotFound, "some export images are no longer available"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	}
	writeProtocolError(w, status, message)
}

func (s *Server) serveExport(w http.ResponseWriter, r *http.Request, plan archive.ExportPlan, mode string) {
	path, err := s.stageExport(r.Context(), plan, mode)
	if err != nil {
		exportError(w, err)
		return
	}
	defer os.Remove(path)
	file, err := os.Open(path)
	if err != nil {
		exportError(w, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		exportError(w, errExportOriginalMissing)
		return
	}
	filename := exportArchiveFilename(plan)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, filename, info.ModTime(), file)
}

func (s *Server) selectedExport(w http.ResponseWriter, r *http.Request) {
	ids, err := parseExportIDs(r.URL.Query()["post_ids"])
	if err == nil {
		var mode string
		mode, err = exportMode(r)
		if err == nil {
			var plan archive.ExportPlan
			plan, err = s.store.ExportPosts(r.Context(), viewerID(r), ids)
			if err == nil {
				s.serveExport(w, r, plan, mode)
				return
			}
		}
	}
	exportError(w, err)
}

func (s *Server) poolExport(w http.ResponseWriter, r *http.Request) {
	mode, err := exportMode(r)
	if err == nil {
		var plan archive.ExportPlan
		plan, err = s.store.ExportPool(r.Context(), viewerID(r), r.PathValue("slug"))
		if err == nil {
			s.serveExport(w, r, plan, mode)
			return
		}
	}
	exportError(w, err)
}
