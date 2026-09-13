package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const MaxBulkTagPosts = 24

var ErrBulkSelectionLimit = errors.New("bulk tag selection exceeds the limit")

type BulkTagPreview struct {
	Posts []BulkTagPreviewPost
}

type BulkTagPreviewPost struct {
	Post                  Post
	Additions, AddNoOps   []Tag
	Removals, RemoveNoOps []Tag
}

func (s *Store) PreviewBulkTagDelta(ctx context.Context, actor User, postIDs []int64, addInput, removeInput string) (BulkTagPreview, error) {
	addTags, removeTags, err := parseBulkTagDelta(addInput, removeInput)
	if err != nil {
		return BulkTagPreview{}, err
	}
	ids, err := normalizeBulkPostIDs(postIDs)
	if err != nil {
		return BulkTagPreview{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return BulkTagPreview{}, err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.CanUpload() {
		return BulkTagPreview{}, ErrPermission
	}
	posts, err := loadBulkTagPosts(ctx, tx, ids)
	if err != nil {
		return BulkTagPreview{}, err
	}
	if err = validateTagCategories(ctx, tx, append(append([]parsedTag{}, addTags...), removeTags...)); err != nil {
		return BulkTagPreview{}, err
	}
	preview := BulkTagPreview{Posts: make([]BulkTagPreviewPost, 0, len(posts))}
	for _, post := range posts {
		changes, err := bulkTagChanges(ctx, tx, post, addTags, removeTags)
		if err != nil {
			return BulkTagPreview{}, err
		}
		preview.Posts = append(preview.Posts, changes)
	}
	return preview, nil
}

func (s *Store) ApplyBulkTagDelta(ctx context.Context, actor User, postIDs []int64, addInput, removeInput string) error {
	addTags, removeTags, err := parseBulkTagDelta(addInput, removeInput)
	if err != nil {
		return err
	}
	ids, err := normalizeBulkPostIDs(postIDs)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.CanUpload() {
		return ErrPermission
	}
	posts, err := loadBulkTagPosts(ctx, tx, ids)
	if err != nil {
		return err
	}
	if err = validateTagCategories(ctx, tx, append(append([]parsedTag{}, addTags...), removeTags...)); err != nil {
		return err
	}
	if err = ensureTags(ctx, tx, addTags); err != nil {
		return err
	}
	for _, post := range posts {
		for _, tag := range addTags {
			if _, err = tx.ExecContext(ctx, `INSERT INTO post_tags(post_id,tag_id) SELECT ?,id FROM tags WHERE name=? ON CONFLICT(post_id,tag_id) DO NOTHING`, post.ID, tag.Name); err != nil {
				return err
			}
		}
		for _, tag := range removeTags {
			if _, err = tx.ExecContext(ctx, `DELETE FROM post_tags WHERE post_id=? AND tag_id=(SELECT id FROM tags WHERE name=?)`, post.ID, tag.Name); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func parseBulkTagDelta(addInput, removeInput string) ([]parsedTag, []parsedTag, error) {
	addTags, err := parseTagInput([]string{addInput})
	if err != nil {
		return nil, nil, err
	}
	removeTags, err := parseTagInput([]string{removeInput})
	if err != nil {
		return nil, nil, err
	}
	removing := make(map[string]struct{}, len(removeTags))
	for _, tag := range removeTags {
		removing[tag.Name] = struct{}{}
	}
	for _, tag := range addTags {
		if _, exists := removing[tag.Name]; exists {
			return nil, nil, fmt.Errorf("%w: tag %q appears in both add and remove", ErrTagCategoryConflict, tag.Name)
		}
	}
	return addTags, removeTags, nil
}

func normalizeBulkPostIDs(raw []int64) ([]int64, error) {
	seen := make(map[int64]struct{}, len(raw))
	ids := make([]int64, 0, len(raw))
	for _, id := range raw {
		if id <= 0 {
			return nil, sql.ErrNoRows
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, sql.ErrNoRows
	}
	if len(ids) > MaxBulkTagPosts {
		return nil, ErrBulkSelectionLimit
	}
	return ids, nil
}

func loadBulkTagPosts(ctx context.Context, tx *sql.Tx, ids []int64) ([]Post, error) {
	posts := make([]Post, 0, len(ids))
	for _, id := range ids {
		var post Post
		var uploader sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT p.id,p.status,p.original_path,p.thumbnail_path,p.mime_type,p.original_filename,p.width,p.height,p.byte_size,p.sha256,p.source,COALESCE(p.published_at,''),p.uploader_id,COALESCE(u.username,'') FROM posts p LEFT JOIN users u ON u.id=p.uploader_id WHERE p.id=? AND p.deleted_at IS NULL`, id).Scan(&post.ID, &post.Status, &post.OriginalPath, &post.ThumbnailPath, &post.MIMEType, &post.OriginalFilename, &post.Width, &post.Height, &post.ByteSize, &post.SHA256, &post.Source, &post.PublishedAt, &uploader, &post.Uploader)
		if err != nil {
			return nil, err
		}
		if uploader.Valid {
			post.UploaderID = uploader.Int64
		}
		rows, err := tx.QueryContext(ctx, `SELECT t.id,t.name,t.display_name,t.category FROM tags t JOIN post_tags pt ON pt.tag_id=t.id WHERE pt.post_id=? ORDER BY t.category,t.name`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var tag Tag
			if err = rows.Scan(&tag.ID, &tag.Name, &tag.DisplayName, &tag.Category); err != nil {
				rows.Close()
				return nil, err
			}
			post.Tags = append(post.Tags, tag)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		posts = append(posts, post)
	}
	return posts, nil
}

func bulkTagChanges(ctx context.Context, tx *sql.Tx, post Post, addTags, removeTags []parsedTag) (BulkTagPreviewPost, error) {
	changes := BulkTagPreviewPost{Post: post}
	for _, tag := range addTags {
		var existing Tag
		err := tx.QueryRowContext(ctx, `SELECT t.id,t.name,t.display_name,t.category FROM tags t WHERE t.name=?`, tag.Name).Scan(&existing.ID, &existing.Name, &existing.DisplayName, &existing.Category)
		if errors.Is(err, sql.ErrNoRows) {
			existing = Tag{Name: tag.Name, DisplayName: strings.ReplaceAll(tag.Name, "_", " "), Category: tag.Category}
			err = nil
		}
		if err != nil {
			return BulkTagPreviewPost{}, err
		}
		var assigned int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM post_tags WHERE post_id=? AND tag_id=?)`, post.ID, existing.ID).Scan(&assigned); err != nil {
			return BulkTagPreviewPost{}, err
		}
		if assigned != 0 {
			changes.AddNoOps = append(changes.AddNoOps, existing)
		} else {
			changes.Additions = append(changes.Additions, existing)
		}
	}
	for _, tag := range removeTags {
		var found Tag
		err := tx.QueryRowContext(ctx, `SELECT t.id,t.name,t.display_name,t.category FROM tags t JOIN post_tags pt ON pt.tag_id=t.id WHERE pt.post_id=? AND t.name=?`, post.ID, tag.Name).Scan(&found.ID, &found.Name, &found.DisplayName, &found.Category)
		if errors.Is(err, sql.ErrNoRows) {
			changes.RemoveNoOps = append(changes.RemoveNoOps, Tag{Name: tag.Name, DisplayName: strings.ReplaceAll(tag.Name, "_", " "), Category: tag.Category})
			continue
		}
		if err != nil {
			return BulkTagPreviewPost{}, err
		}
		changes.Removals = append(changes.Removals, found)
	}
	return changes, nil
}
