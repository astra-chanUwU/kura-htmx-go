package archive

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct{ DB *sql.DB }

type Post struct {
	ID                                            int64
	Status, OriginalPath, ThumbnailPath, MIMEType string
	Width, Height                                 int
	ByteSize                                      int64
	SHA256, Source, PublishedAt                   string
	UploaderID                                    int64
	Uploader                                      string
	Favorite                                      bool
	Tags                                          []Tag
}

type Tag struct {
	ID                          int64
	Name, DisplayName, Category string
	Count                       int
}
type Pool struct {
	ID                              int64
	Slug, Name, Description, Status string
	OwnerID                         int64
	Owner                           string
	Count                           int
	ContainsPost                    bool
	Posts                           []Post
}
type PostPage struct {
	Posts                       []Post
	Total, Page, PerPage, Pages int
	Query                       string
}

type PoolCandidateFilter struct {
	Source   string
	PoolSlug string
	Query    string
	ViewerID int64
	Page     int
	PerPage  int
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{DB: db}
	if err = s.Migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return err
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		var exists int
		if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=?`, e.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(body)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (?)`, e.Name())
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func normalizeQuery(q string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range strings.Fields(strings.ToLower(q)) {
		t = strings.Trim(t, "#,")
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func (s *Store) ListPosts(ctx context.Context, query string, page, perPage int) (PostPage, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 24
	}
	tags := normalizeQuery(query)
	where := `p.status='published' AND p.deleted_at IS NULL`
	args := []any{}
	for _, tag := range tags {
		where += ` AND EXISTS (SELECT 1 FROM post_tags pt JOIN tags t ON t.id=pt.tag_id WHERE pt.post_id=p.id AND t.name=?)`
		args = append(args, tag)
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM posts p WHERE `+where, args...).Scan(&total); err != nil {
		return PostPage{}, err
	}
	pages := (total + perPage - 1) / perPage
	if pages == 0 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	qargs := append(append([]any{}, args...), perPage, (page-1)*perPage)
	rows, err := s.DB.QueryContext(ctx, `SELECT p.id,p.status,p.original_path,p.thumbnail_path,p.mime_type,p.width,p.height,p.byte_size,p.sha256,p.source,COALESCE(p.published_at,'') FROM posts p WHERE `+where+` ORDER BY p.published_at DESC,p.id DESC LIMIT ? OFFSET ?`, qargs...)
	if err != nil {
		return PostPage{}, err
	}
	defer rows.Close()
	result := PostPage{Total: total, Page: page, PerPage: perPage, Pages: pages, Query: strings.Join(tags, " ")}
	for rows.Next() {
		var p Post
		if err = rows.Scan(&p.ID, &p.Status, &p.OriginalPath, &p.ThumbnailPath, &p.MIMEType, &p.Width, &p.Height, &p.ByteSize, &p.SHA256, &p.Source, &p.PublishedAt); err != nil {
			return PostPage{}, err
		}
		result.Posts = append(result.Posts, p)
	}
	return result, rows.Err()
}

func (s *Store) PoolCandidates(ctx context.Context, filter PoolCandidateFilter) (PostPage, error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PerPage < 1 || filter.PerPage > 100 {
		filter.PerPage = 100
	}
	source := filter.Source
	if source == "" {
		source = "all"
	}
	tags := normalizeQuery(filter.Query)
	from := "posts p"
	where := `p.status='published' AND p.deleted_at IS NULL`
	args := []any{}
	order := "p.published_at DESC,p.id DESC"
	switch source {
	case "all":
	case "favorites":
		from += " JOIN favorites f ON f.post_id=p.id"
		where += " AND f.user_id=?"
		args = append(args, filter.ViewerID)
		order = "f.created_at DESC,p.id DESC"
	case "pool":
		var poolID int64
		err := s.DB.QueryRowContext(ctx, `SELECT id FROM pools WHERE slug=? AND (status='published' OR owner_id=?)`, filter.PoolSlug, filter.ViewerID).Scan(&poolID)
		if err != nil {
			return PostPage{}, err
		}
		from += " JOIN pool_posts pp ON pp.post_id=p.id"
		where += " AND pp.pool_id=?"
		args = append(args, poolID)
		order = "pp.position ASC,p.id ASC"
	default:
		return PostPage{}, errors.New("invalid pool candidate source")
	}
	for _, tag := range tags {
		where += ` AND EXISTS (SELECT 1 FROM post_tags pt JOIN tags t ON t.id=pt.tag_id WHERE pt.post_id=p.id AND t.name=?)`
		args = append(args, tag)
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM `+from+` WHERE `+where, args...).Scan(&total); err != nil {
		return PostPage{}, err
	}
	pages := (total + filter.PerPage - 1) / filter.PerPage
	if pages == 0 {
		pages = 1
	}
	if filter.Page > pages {
		filter.Page = pages
	}
	qargs := append(append([]any{}, args...), filter.PerPage, (filter.Page-1)*filter.PerPage)
	rows, err := s.DB.QueryContext(ctx, `SELECT p.id,p.status,p.original_path,p.thumbnail_path,p.mime_type,p.width,p.height,p.byte_size,p.sha256,p.source,COALESCE(p.published_at,'') FROM `+from+` WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, qargs...)
	if err != nil {
		return PostPage{}, err
	}
	defer rows.Close()
	result := PostPage{Total: total, Page: filter.Page, PerPage: filter.PerPage, Pages: pages, Query: strings.Join(tags, " ")}
	for rows.Next() {
		var p Post
		if err = rows.Scan(&p.ID, &p.Status, &p.OriginalPath, &p.ThumbnailPath, &p.MIMEType, &p.Width, &p.Height, &p.ByteSize, &p.SHA256, &p.Source, &p.PublishedAt); err != nil {
			return PostPage{}, err
		}
		result.Posts = append(result.Posts, p)
	}
	return result, rows.Err()
}

func (s *Store) Post(ctx context.Context, id int64) (Post, error) {
	return s.PostForUser(ctx, id, 0, false)
}

func (s *Store) PostForUser(ctx context.Context, id, viewerID int64, canModerate bool) (Post, error) {
	var p Post
	var uploader sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT p.id,p.status,p.original_path,p.thumbnail_path,p.mime_type,p.width,p.height,p.byte_size,p.sha256,p.source,COALESCE(p.published_at,''),p.uploader_id,COALESCE(u.username,'') FROM posts p LEFT JOIN users u ON u.id=p.uploader_id WHERE p.id=? AND p.deleted_at IS NULL AND (p.status='published' OR p.uploader_id=? OR ?)`, id, viewerID, canModerate).Scan(&p.ID, &p.Status, &p.OriginalPath, &p.ThumbnailPath, &p.MIMEType, &p.Width, &p.Height, &p.ByteSize, &p.SHA256, &p.Source, &p.PublishedAt, &uploader, &p.Uploader)
	if err != nil {
		return p, err
	}
	if uploader.Valid {
		p.UploaderID = uploader.Int64
	}
	if viewerID != 0 {
		_ = s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM favorites WHERE post_id=? AND user_id=?)`, id, viewerID).Scan(&p.Favorite)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT t.id,t.name,t.display_name,t.category FROM tags t JOIN post_tags pt ON pt.tag_id=t.id WHERE pt.post_id=? ORDER BY t.category,t.name`, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var t Tag
		if err = rows.Scan(&t.ID, &t.Name, &t.DisplayName, &t.Category); err != nil {
			return p, err
		}
		p.Tags = append(p.Tags, t)
	}
	return p, rows.Err()
}

func (s *Store) Tags(ctx context.Context) ([]Tag, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT t.id,t.name,t.display_name,t.category,count(p.id) FROM tags t LEFT JOIN post_tags pt ON pt.tag_id=t.id LEFT JOIN posts p ON p.id=pt.post_id AND p.status='published' AND p.deleted_at IS NULL GROUP BY t.id HAVING count(p.id)>0 ORDER BY t.category,count(p.id) DESC,t.name LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tag
	for rows.Next() {
		var t Tag
		if err = rows.Scan(&t.ID, &t.Name, &t.DisplayName, &t.Category, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) Pools(ctx context.Context) ([]Pool, error) {
	return s.PoolsForUser(ctx, 0)
}
func (s *Store) PoolsForUser(ctx context.Context, viewerID int64) ([]Pool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT po.id,po.slug,po.name,po.description,po.status,COALESCE(po.owner_id,0),COALESCE(u.username,''),count(CASE WHEN p.status='published' AND p.deleted_at IS NULL THEN 1 END) FROM pools po LEFT JOIN users u ON u.id=po.owner_id LEFT JOIN pool_posts pp ON pp.pool_id=po.id LEFT JOIN posts p ON p.id=pp.post_id WHERE po.status='published' OR po.owner_id=? GROUP BY po.id ORDER BY po.status,po.name`, viewerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pool
	for rows.Next() {
		var p Pool
		if err = rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.Status, &p.OwnerID, &p.Owner, &p.Count); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) RandomPostID(ctx context.Context) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM posts WHERE status='published' AND deleted_at IS NULL ORDER BY random() LIMIT 1`).Scan(&id)
	return id, err
}
