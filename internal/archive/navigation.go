package archive

import (
	"context"
	"database/sql"
	"errors"
)

var ErrNavigationUnavailable = errors.New("post navigation context is unavailable")

type PostNavigationContext struct {
	Source     string
	Query      string
	Sort       string
	PoolSlug   string
	Status     string
	UploaderID int64
}

type PostNavigation struct {
	PreviousID int64
	NextID     int64
}

func (s *Store) PostNeighbors(ctx context.Context, currentID, viewerID int64, navigation PostNavigationContext) (PostNavigation, error) {
	if currentID < 1 {
		return PostNavigation{}, ErrNavigationUnavailable
	}
	switch navigation.Source {
	case "browse":
		return s.browseNeighbors(ctx, currentID, navigation.Query, navigation.Sort)
	case "pool":
		return s.poolNeighbors(ctx, currentID, viewerID, navigation.PoolSlug)
	case "uploads":
		return s.uploadNeighbors(ctx, currentID, viewerID, navigation.Status)
	case "admin":
		return s.adminNeighbors(ctx, currentID, viewerID, navigation.Status, navigation.UploaderID)
	default:
		return PostNavigation{}, ErrNavigationUnavailable
	}
}

func (s *Store) browseNeighbors(ctx context.Context, currentID int64, query, sort string) (PostNavigation, error) {
	search, err := ParseSearchQuery(query)
	if err != nil {
		return PostNavigation{}, ErrNavigationUnavailable
	}
	if sort == "" {
		sort = SearchSortNewest
	}
	if sort != SearchSortNewest && sort != SearchSortOldest {
		return PostNavigation{}, ErrNavigationUnavailable
	}
	where := `p.status='published' AND p.deleted_at IS NULL AND p.quarantined_at IS NULL`
	args := []any{}
	where, args = searchPredicates(where, args, search)
	var publishedAt string
	currentArgs := append([]any{currentID}, args...)
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(p.published_at,'') FROM posts p WHERE p.id=? AND `+where, currentArgs...).Scan(&publishedAt); errors.Is(err, sql.ErrNoRows) {
		return PostNavigation{}, ErrNavigationUnavailable
	} else if err != nil {
		return PostNavigation{}, err
	}
	previousArgs := append(append([]any{}, args...), publishedAt, publishedAt, currentID)
	var previous int64
	previousSQL := `SELECT p.id FROM posts p WHERE ` + where
	nextSQL := `SELECT p.id FROM posts p WHERE ` + where
	if sort == SearchSortOldest {
		previousSQL += ` AND (p.published_at<? OR (p.published_at=? AND p.id<?)) ORDER BY p.published_at DESC,p.id DESC LIMIT 1`
		nextSQL += ` AND (p.published_at>? OR (p.published_at=? AND p.id>?)) ORDER BY p.published_at ASC,p.id ASC LIMIT 1`
	} else {
		previousSQL += ` AND (p.published_at>? OR (p.published_at=? AND p.id>?)) ORDER BY p.published_at ASC,p.id ASC LIMIT 1`
		nextSQL += ` AND (p.published_at<? OR (p.published_at=? AND p.id<?)) ORDER BY p.published_at DESC,p.id DESC LIMIT 1`
	}
	err = s.DB.QueryRowContext(ctx, previousSQL, previousArgs...).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		previous = 0
	} else if err != nil {
		return PostNavigation{}, err
	}
	nextArgs := append(append([]any{}, args...), publishedAt, publishedAt, currentID)
	var next int64
	err = s.DB.QueryRowContext(ctx, nextSQL, nextArgs...).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		next = 0
	} else if err != nil {
		return PostNavigation{}, err
	}
	return PostNavigation{PreviousID: previous, NextID: next}, nil
}

func (s *Store) poolNeighbors(ctx context.Context, currentID, viewerID int64, slug string) (PostNavigation, error) {
	if slug == "" {
		return PostNavigation{}, ErrNavigationUnavailable
	}
	var poolID int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM pools WHERE slug=? AND (status='published' OR (owner_id=? AND EXISTS(SELECT 1 FROM users v WHERE v.id=? AND v.suspended_at IS NULL)))`, slug, viewerID, viewerID).Scan(&poolID)
	if errors.Is(err, sql.ErrNoRows) {
		return PostNavigation{}, ErrNavigationUnavailable
	}
	if err != nil {
		return PostNavigation{}, err
	}
	var position int
	if err = s.DB.QueryRowContext(ctx, `SELECT pp.position FROM pool_posts pp JOIN posts p ON p.id=pp.post_id WHERE pp.pool_id=? AND p.id=? AND p.status='published' AND p.deleted_at IS NULL AND p.quarantined_at IS NULL`, poolID, currentID).Scan(&position); errors.Is(err, sql.ErrNoRows) {
		return PostNavigation{}, ErrNavigationUnavailable
	} else if err != nil {
		return PostNavigation{}, err
	}
	var previous int64
	err = s.DB.QueryRowContext(ctx, `SELECT p.id FROM pool_posts pp JOIN posts p ON p.id=pp.post_id WHERE pp.pool_id=? AND pp.position<? AND p.status='published' AND p.deleted_at IS NULL AND p.quarantined_at IS NULL ORDER BY pp.position DESC LIMIT 1`, poolID, position).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		previous = 0
	} else if err != nil {
		return PostNavigation{}, err
	}
	var next int64
	err = s.DB.QueryRowContext(ctx, `SELECT p.id FROM pool_posts pp JOIN posts p ON p.id=pp.post_id WHERE pp.pool_id=? AND pp.position>? AND p.status='published' AND p.deleted_at IS NULL AND p.quarantined_at IS NULL ORDER BY pp.position ASC LIMIT 1`, poolID, position).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		next = 0
	} else if err != nil {
		return PostNavigation{}, err
	}
	return PostNavigation{PreviousID: previous, NextID: next}, nil
}

func (s *Store) uploadNeighbors(ctx context.Context, currentID, viewerID int64, status string) (PostNavigation, error) {
	current, err := s.User(ctx, viewerID)
	if err != nil || !current.CanUpload() {
		return PostNavigation{}, ErrNavigationUnavailable
	}
	if status != "all" && status != "draft" && status != "published" {
		return PostNavigation{}, ErrNavigationUnavailable
	}
	where := "p.uploader_id=? AND p.deleted_at IS NULL AND p.quarantined_at IS NULL"
	args := []any{current.ID}
	if status != "all" {
		where += " AND p.status=?"
		args = append(args, status)
	}
	return s.idNeighbors(ctx, currentID, where, args...)
}

func (s *Store) adminNeighbors(ctx context.Context, currentID, viewerID int64, status string, uploaderID int64) (PostNavigation, error) {
	current, err := s.User(ctx, viewerID)
	if err != nil || !current.Active() || !current.IsSuperAdmin {
		return PostNavigation{}, ErrNavigationUnavailable
	}
	if status != "all" && status != "draft" && status != "published" {
		return PostNavigation{}, ErrNavigationUnavailable
	}
	where := "p.deleted_at IS NULL AND p.quarantined_at IS NULL"
	args := []any{}
	if status != "all" {
		where += " AND p.status=?"
		args = append(args, status)
	}
	if uploaderID > 0 {
		where += " AND p.uploader_id=?"
		args = append(args, uploaderID)
	}
	return s.idNeighbors(ctx, currentID, where, args...)
}

func (s *Store) idNeighbors(ctx context.Context, currentID int64, where string, args ...any) (PostNavigation, error) {
	currentArgs := append([]any{currentID}, args...)
	var exists int
	if err := s.DB.QueryRowContext(ctx, `SELECT 1 FROM posts p WHERE p.id=? AND `+where, currentArgs...).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return PostNavigation{}, ErrNavigationUnavailable
	} else if err != nil {
		return PostNavigation{}, err
	}
	previousArgs := append(append([]any{}, args...), currentID)
	var previous int64
	err := s.DB.QueryRowContext(ctx, `SELECT p.id FROM posts p WHERE `+where+` AND p.id>? ORDER BY p.id ASC LIMIT 1`, previousArgs...).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		previous = 0
	} else if err != nil {
		return PostNavigation{}, err
	}
	nextArgs := append(append([]any{}, args...), currentID)
	var next int64
	err = s.DB.QueryRowContext(ctx, `SELECT p.id FROM posts p WHERE `+where+` AND p.id<? ORDER BY p.id DESC LIMIT 1`, nextArgs...).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		next = 0
	} else if err != nil {
		return PostNavigation{}, err
	}
	return PostNavigation{PreviousID: previous, NextID: next}, nil
}
