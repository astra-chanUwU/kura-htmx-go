package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var slugCleanup = regexp.MustCompile(`[^a-z0-9]+`)

type NewPost struct {
	UploaderID                          int64
	Status, OriginalPath, ThumbnailPath string
	MIMEType, SHA256, Source            string
	Width, Height                       int
	ByteSize                            int64
	Tags                                []string
}

func normalizeTags(raw []string) []string {
	seen := map[string]bool{}
	var tags []string
	for _, part := range raw {
		for _, tag := range strings.Fields(strings.ToLower(part)) {
			tag = strings.Trim(tag, "#, ")
			tag = slugCleanup.ReplaceAllString(tag, "_")
			tag = strings.Trim(tag, "_")
			if tag != "" && len(tag) <= 80 && !seen[tag] {
				seen[tag] = true
				tags = append(tags, tag)
			}
		}
	}
	sort.Strings(tags)
	return tags
}

func (s *Store) PostIDByHash(ctx context.Context, hash string) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM posts WHERE sha256=?`, hash).Scan(&id)
	return id, err
}

func (s *Store) CreatePost(ctx context.Context, input NewPost) (Post, error) {
	if input.Status != "draft" && input.Status != "published" {
		return Post{}, errors.New("invalid post status")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Post{}, err
	}
	defer tx.Rollback()
	var published any
	if input.Status == "published" {
		published = time.Now().UTC().Format(time.RFC3339)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,source,published_at,uploader_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, input.Status, input.OriginalPath, input.ThumbnailPath, input.MIMEType, input.Width, input.Height, input.ByteSize, input.SHA256, strings.TrimSpace(input.Source), published, input.UploaderID)
	if err != nil {
		return Post{}, err
	}
	id, _ := result.LastInsertId()
	for _, tag := range normalizeTags(input.Tags) {
		if _, err = tx.ExecContext(ctx, `INSERT INTO tags(name,display_name,category) VALUES(?,?,'general') ON CONFLICT(name) DO NOTHING`, tag, strings.ReplaceAll(tag, "_", " ")); err != nil {
			return Post{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO post_tags(post_id,tag_id) SELECT ?,id FROM tags WHERE name=?`, id, tag); err != nil {
			return Post{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Post{}, err
	}
	return s.PostForUser(ctx, id, input.UploaderID, true)
}

func (s *Store) UpdatePost(ctx context.Context, id int64, source, tags, status string) error {
	if status != "draft" && status != "published" {
		return errors.New("invalid post status")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var published any
	if status == "published" {
		published = time.Now().UTC().Format(time.RFC3339)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE posts SET source=?,status=?,published_at=CASE WHEN ?='published' THEN COALESCE(published_at,?) ELSE NULL END WHERE id=? AND deleted_at IS NULL`, strings.TrimSpace(source), status, status, published, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM post_tags WHERE post_id=?`, id); err != nil {
		return err
	}
	for _, tag := range normalizeTags([]string{tags}) {
		if _, err = tx.ExecContext(ctx, `INSERT INTO tags(name,display_name,category) VALUES(?,?,'general') ON CONFLICT(name) DO NOTHING`, tag, strings.ReplaceAll(tag, "_", " ")); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO post_tags(post_id,tag_id) SELECT ?,id FROM tags WHERE name=?`, id, tag); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SoftDeletePost(ctx context.Context, id int64) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE posts SET deleted_at=? WHERE id=? AND deleted_at IS NULL`, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SetFavorite(ctx context.Context, userID, postID int64, favorite bool) error {
	if favorite {
		_, err := s.DB.ExecContext(ctx, `INSERT INTO favorites(post_id,user_id) SELECT id,? FROM posts WHERE id=? AND status='published' AND deleted_at IS NULL ON CONFLICT(post_id,user_id) DO NOTHING`, userID, postID)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM favorites WHERE user_id=? AND post_id=?`, userID, postID)
	return err
}

func (s *Store) Favorites(ctx context.Context, userID int64) ([]Post, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT p.id,p.status,p.original_path,p.thumbnail_path,p.mime_type,p.width,p.height,p.byte_size,p.sha256,p.source,COALESCE(p.published_at,'') FROM posts p JOIN favorites f ON f.post_id=p.id WHERE f.user_id=? AND p.status='published' AND p.deleted_at IS NULL ORDER BY f.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var posts []Post
	for rows.Next() {
		var p Post
		if err := rows.Scan(&p.ID, &p.Status, &p.OriginalPath, &p.ThumbnailPath, &p.MIMEType, &p.Width, &p.Height, &p.ByteSize, &p.SHA256, &p.Source, &p.PublishedAt); err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	return posts, rows.Err()
}

func poolSlug(name string) string {
	slug := slugCleanup.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	return strings.Trim(slug, "-")
}

func (s *Store) CreatePool(ctx context.Context, ownerID int64, name, description, status, postIDs string) (Pool, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 || (status != "draft" && status != "published") {
		return Pool{}, errors.New("invalid pool")
	}
	slug := poolSlug(name)
	if slug == "" {
		return Pool{}, errors.New("pool name needs letters or numbers")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Pool{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO pools(slug,name,description,owner_id,status) VALUES(?,?,?,?,?)`, slug, name, strings.TrimSpace(description), ownerID, status)
	if err != nil {
		return Pool{}, err
	}
	id, _ := result.LastInsertId()
	if err = replacePoolPosts(ctx, tx, id, postIDs); err != nil {
		return Pool{}, err
	}
	if err = tx.Commit(); err != nil {
		return Pool{}, err
	}
	return s.Pool(ctx, slug, ownerID)
}

func (s *Store) AddPostToPool(ctx context.Context, ownerID int64, slug string, postID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var poolID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM pools WHERE slug=? AND owner_id=?`, slug, ownerID).Scan(&poolID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPermission
		}
		return err
	}
	var available int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM posts WHERE id=? AND status='published' AND deleted_at IS NULL)`, postID).Scan(&available); err != nil {
		return err
	}
	if available == 0 {
		return sql.ErrNoRows
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pool_posts(pool_id,post_id,position) VALUES(?,?,COALESCE((SELECT max(position)+1 FROM pool_posts WHERE pool_id=?),1)) ON CONFLICT(pool_id,post_id) DO NOTHING`, poolID, postID, poolID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) OwnedPoolsForPost(ctx context.Context, ownerID, postID int64) ([]Pool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT po.id,po.slug,po.name,po.description,po.status,po.owner_id,EXISTS(SELECT 1 FROM pool_posts pp WHERE pp.pool_id=po.id AND pp.post_id=?) FROM pools po WHERE po.owner_id=? ORDER BY po.status,po.name`, postID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pools []Pool
	for rows.Next() {
		var pool Pool
		if err := rows.Scan(&pool.ID, &pool.Slug, &pool.Name, &pool.Description, &pool.Status, &pool.OwnerID, &pool.ContainsPost); err != nil {
			return nil, err
		}
		pools = append(pools, pool)
	}
	return pools, rows.Err()
}

func replacePoolPosts(ctx context.Context, tx *sql.Tx, poolID int64, raw string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM pool_posts WHERE pool_id=?`, poolID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	position := 0
	for _, field := range strings.Fields(strings.NewReplacer(",", " ", "#", " ").Replace(raw)) {
		var id int64
		if _, err := fmt.Sscan(field, &id); err != nil || id < 1 || seen[id] {
			continue
		}
		seen[id] = true
		position++
		result, err := tx.ExecContext(ctx, `INSERT INTO pool_posts(pool_id,post_id,position) SELECT ?,id,? FROM posts WHERE id=? AND status='published' AND deleted_at IS NULL`, poolID, position, id)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return errors.New("pool contains an unavailable post")
		}
	}
	return nil
}

func (s *Store) Pool(ctx context.Context, slug string, viewerID int64) (Pool, error) {
	var pool Pool
	err := s.DB.QueryRowContext(ctx, `SELECT po.id,po.slug,po.name,po.description,po.status,COALESCE(po.owner_id,0),COALESCE(u.username,'') FROM pools po LEFT JOIN users u ON u.id=po.owner_id WHERE po.slug=? AND (po.status='published' OR po.owner_id=?)`, slug, viewerID).Scan(&pool.ID, &pool.Slug, &pool.Name, &pool.Description, &pool.Status, &pool.OwnerID, &pool.Owner)
	if err != nil {
		return pool, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT p.id,p.status,p.original_path,p.thumbnail_path,p.mime_type,p.width,p.height,p.byte_size,p.sha256,p.source,COALESCE(p.published_at,'') FROM pool_posts pp JOIN posts p ON p.id=pp.post_id WHERE pp.pool_id=? AND p.status='published' AND p.deleted_at IS NULL ORDER BY pp.position`, pool.ID)
	if err != nil {
		return pool, err
	}
	defer rows.Close()
	for rows.Next() {
		var p Post
		if err := rows.Scan(&p.ID, &p.Status, &p.OriginalPath, &p.ThumbnailPath, &p.MIMEType, &p.Width, &p.Height, &p.ByteSize, &p.SHA256, &p.Source, &p.PublishedAt); err != nil {
			return pool, err
		}
		pool.Posts = append(pool.Posts, p)
	}
	pool.Count = len(pool.Posts)
	return pool, rows.Err()
}

func (s *Store) UpdatePool(ctx context.Context, ownerID int64, slug, name, description, status, postIDs string) error {
	if strings.TrimSpace(name) == "" || (status != "draft" && status != "published") {
		return errors.New("invalid pool")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE pools SET name=?,description=?,status=? WHERE slug=? AND owner_id=?`, strings.TrimSpace(name), strings.TrimSpace(description), status, slug, ownerID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrPermission
	}
	var id int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM pools WHERE slug=?`, slug).Scan(&id); err != nil {
		return err
	}
	if err = replacePoolPosts(ctx, tx, id, postIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func (p Pool) PostIDs() string {
	ids := make([]string, 0, len(p.Posts))
	for _, post := range p.Posts {
		ids = append(ids, strconv.FormatInt(post.ID, 10))
	}
	return strings.Join(ids, " ")
}
