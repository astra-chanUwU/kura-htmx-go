package archive

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

const maxQuarantineReasonLength = 500

func scanPost(row interface{ Scan(...any) error }) (Post, error) {
	var post Post
	var uploaderID, quarantinedBy sql.NullInt64
	var err error
	err = row.Scan(
		&post.ID, &post.Status, &post.OriginalPath, &post.ThumbnailPath, &post.MIMEType, &post.OriginalFilename,
		&post.Width, &post.Height, &post.ByteSize, &post.SHA256, &post.Source, &post.PublishedAt,
		&uploaderID, &post.Uploader, &post.DeletedAt, &post.QuarantinedAt, &quarantinedBy, &post.QuarantineReason, &post.QuarantinePreviousStatus,
	)
	if uploaderID.Valid {
		post.UploaderID = uploaderID.Int64
	}
	if quarantinedBy.Valid {
		post.QuarantinedBy = quarantinedBy.Int64
	}
	return post, err
}

const postReviewSelect = `SELECT p.id,p.status,p.original_path,p.thumbnail_path,p.mime_type,p.original_filename,p.width,p.height,p.byte_size,p.sha256,p.source,COALESCE(p.published_at,''),p.uploader_id,COALESCE(u.username,''),COALESCE(p.deleted_at,''),COALESCE(p.quarantined_at,''),p.quarantined_by,COALESCE(p.quarantine_reason,''),COALESCE(p.quarantine_previous_status,'') FROM posts p LEFT JOIN users u ON u.id=p.uploader_id`

func (s *Store) PostForReview(ctx context.Context, actor User, id int64) (Post, error) {
	current, err := s.User(ctx, actor.ID)
	if err != nil || !current.Active() || !current.IsSuperAdmin {
		return Post{}, ErrPermission
	}
	post, err := scanPost(s.DB.QueryRowContext(ctx, postReviewSelect+` WHERE p.id=? AND p.deleted_at IS NULL AND p.quarantined_at IS NOT NULL`, id))
	if err != nil {
		return Post{}, err
	}
	if err = s.loadPostTags(ctx, &post); err != nil {
		return Post{}, err
	}
	return post, nil
}

func (s *Store) PostForDeletion(ctx context.Context, actor User, id int64) (Post, error) {
	current, err := s.User(ctx, actor.ID)
	if err != nil || !current.Active() || !current.CanUpload() {
		return Post{}, ErrPermission
	}
	post, err := scanPost(s.DB.QueryRowContext(ctx, postReviewSelect+` WHERE p.id=?`, id))
	if err != nil {
		return Post{}, err
	}
	if post.QuarantinedAt != "" && !current.IsSuperAdmin {
		return Post{}, ErrPermission
	}
	if !current.IsSuperAdmin && (post.UploaderID == 0 || post.UploaderID != current.ID) {
		return Post{}, ErrPermission
	}
	return post, nil
}

func (s *Store) loadPostTags(ctx context.Context, post *Post) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT t.id,t.name,t.display_name,t.category FROM tags t JOIN post_tags pt ON pt.tag_id=t.id WHERE pt.post_id=? ORDER BY t.category,t.name`, post.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var tag Tag
		if err = rows.Scan(&tag.ID, &tag.Name, &tag.DisplayName, &tag.Category); err != nil {
			return err
		}
		post.Tags = append(post.Tags, tag)
	}
	return rows.Err()
}

func quarantineReason(raw string) string {
	reason := strings.TrimSpace(raw)
	if reason == "" {
		reason = "manual quarantine"
	}
	if len([]rune(reason)) > maxQuarantineReasonLength {
		reason = string([]rune(reason)[:maxQuarantineReasonLength])
	}
	return reason
}

func (s *Store) QuarantinePost(ctx context.Context, actor User, id int64, reason string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.CanUpload() {
		return ErrPermission
	}
	var uploaderID sql.NullInt64
	var status string
	var deletedAt, quarantinedAt sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT status,uploader_id,deleted_at,quarantined_at FROM posts WHERE id=?`, id).Scan(&status, &uploaderID, &deletedAt, &quarantinedAt); err != nil {
		return err
	}
	if deletedAt.Valid || quarantinedAt.Valid || (!current.IsSuperAdmin && (!uploaderID.Valid || (uploaderID.Int64 != current.ID && current.Role != "admin"))) {
		return ErrPermission
	}
	now := time.Now().UTC().Format(time.RFC3339)
	reason = quarantineReason(reason)
	if _, err = tx.ExecContext(ctx, `UPDATE posts SET quarantined_at=?,quarantined_by=?,quarantine_reason=?,quarantine_previous_status=? WHERE id=? AND deleted_at IS NULL AND quarantined_at IS NULL`, now, current.ID, reason, status, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(event_type,actor_id,post_id,reason,created_at) VALUES('quarantine',?,?,?,?)`, current.ID, id, reason, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RestorePost(ctx context.Context, actor User, id int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.IsSuperAdmin {
		return ErrPermission
	}
	var previous, status string
	if err = tx.QueryRowContext(ctx, `SELECT status,COALESCE(quarantine_previous_status,'') FROM posts WHERE id=? AND deleted_at IS NULL AND quarantined_at IS NOT NULL`, id).Scan(&status, &previous); err != nil {
		return err
	}
	if previous != "draft" && previous != "published" {
		previous = status
	}
	publishedAt := any(nil)
	if previous == "published" {
		publishedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE posts SET status=?,published_at=CASE WHEN ?='published' THEN COALESCE(published_at,?) ELSE NULL END,quarantined_at=NULL,quarantined_by=NULL,quarantine_reason='',quarantine_previous_status=NULL WHERE id=? AND deleted_at IS NULL AND quarantined_at IS NOT NULL`, previous, previous, publishedAt, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(event_type,actor_id,post_id,reason,created_at) VALUES('restore',?,?,?,?)`, current.ID, id, "quarantine restored", time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PermanentDeletePost(ctx context.Context, actor User, id int64) (Post, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Post{}, err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.CanUpload() {
		return Post{}, ErrPermission
	}
	post, err := scanPost(tx.QueryRowContext(ctx, postReviewSelect+` WHERE p.id=?`, id))
	if err != nil {
		return Post{}, err
	}
	if post.QuarantinedAt != "" && !current.IsSuperAdmin {
		return Post{}, ErrPermission
	}
	if !current.IsSuperAdmin && (post.UploaderID == 0 || post.UploaderID != current.ID) {
		return Post{}, ErrPermission
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM posts WHERE id=?`, id); err != nil {
		return Post{}, err
	}
	return post, tx.Commit()
}
