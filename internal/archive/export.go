package archive

import (
	"context"
	"database/sql"
	"errors"
)

const (
	ExportMaxPosts = 100
	ExportMaxBytes = 512 << 20
)

var (
	ErrExportInvalidSelection = errors.New("invalid export selection")
	ErrExportSelectionLimit   = errors.New("export selection exceeds the image limit")
	ErrExportBytesLimit       = errors.New("export selection exceeds the byte limit")
	ErrExportUnavailable      = errors.New("an export image is unavailable")
	ErrExportUnsupportedMIME  = errors.New("export image has an unsupported MIME type")
)

type ExportSource struct {
	Type, Slug, Name, Status string
}

type ExportPlan struct {
	Source     ExportSource
	Posts      []Post
	TotalBytes int64
}

func (s *Store) ExportPosts(ctx context.Context, viewerID int64, ids []int64) (ExportPlan, error) {
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id < 1 {
			return ExportPlan{}, ErrExportInvalidSelection
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return ExportPlan{}, ErrExportInvalidSelection
	}
	if len(unique) > ExportMaxPosts {
		return ExportPlan{}, ErrExportSelectionLimit
	}
	return s.exportPosts(ctx, viewerID, unique, ExportSource{Type: "selected"})
}

func (s *Store) ExportPool(ctx context.Context, viewerID int64, slug string) (ExportPlan, error) {
	var source ExportSource
	var poolID int64
	err := s.DB.QueryRowContext(ctx, `SELECT id,slug,name,status FROM pools WHERE slug=? AND (status='published' OR (owner_id=? AND EXISTS(SELECT 1 FROM users v WHERE v.id=? AND v.suspended_at IS NULL)))`, slug, viewerID, viewerID).Scan(&poolID, &source.Slug, &source.Name, &source.Status)
	if err != nil {
		return ExportPlan{}, err
	}
	source.Type = "pool"
	rows, err := s.DB.QueryContext(ctx, `SELECT p.id,p.status,p.deleted_at FROM pool_posts pp JOIN posts p ON p.id=pp.post_id WHERE pp.pool_id=? ORDER BY pp.position`, poolID)
	if err != nil {
		return ExportPlan{}, err
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		var status string
		var deletedAt sql.NullString
		if err := rows.Scan(&id, &status, &deletedAt); err != nil {
			return ExportPlan{}, err
		}
		if status != "published" || deletedAt.Valid {
			return ExportPlan{}, ErrExportUnavailable
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return ExportPlan{}, err
	}
	if len(ids) == 0 {
		return ExportPlan{}, ErrExportInvalidSelection
	}
	if len(ids) > ExportMaxPosts {
		return ExportPlan{}, ErrExportSelectionLimit
	}
	plan, err := s.exportPosts(ctx, viewerID, ids, source)
	if errors.Is(err, sql.ErrNoRows) {
		return ExportPlan{}, ErrExportUnavailable
	}
	return plan, err
}

func (s *Store) exportPosts(ctx context.Context, viewerID int64, ids []int64, source ExportSource) (ExportPlan, error) {
	plan := ExportPlan{Source: source, Posts: make([]Post, 0, len(ids))}
	for _, id := range ids {
		post, err := s.PostForUser(ctx, id, viewerID, false)
		if err != nil {
			return ExportPlan{}, err
		}
		if post.MIMEType != "image/jpeg" && post.MIMEType != "image/png" && post.MIMEType != "image/gif" {
			return ExportPlan{}, ErrExportUnsupportedMIME
		}
		if post.ByteSize < 0 || post.ByteSize > ExportMaxBytes-plan.TotalBytes {
			return ExportPlan{}, ErrExportBytesLimit
		}
		plan.TotalBytes += post.ByteSize
		plan.Posts = append(plan.Posts, post)
	}
	return plan, nil
}
