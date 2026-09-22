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
	ID                                                              int64
	Status, OriginalPath, ThumbnailPath, MIMEType, OriginalFilename string
	Width, Height                                                   int
	ByteSize                                                        int64
	SHA256, Source, PublishedAt, DeletedAt, QuarantinedAt           string
	QuarantineReason, QuarantinePreviousStatus                      string
	QuarantinedBy                                                   int64
	UploaderID                                                      int64
	Uploader                                                        string
	Favorite                                                        bool
	Tags                                                            []Tag
}

type Tag struct {
	ID                          int64
	Name, DisplayName, Category string
	Count                       int
}

const tagSuggestionLimit = 8

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

type AdminPostFilter struct {
	Status     string
	UploaderID int64
	Page       int
	PerPage    int
}

type UploaderPostFilter struct {
	Status  string
	Page    int
	PerPage int
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
		rebuildsUsers := e.Name() == "003_auth_security.sql"
		if rebuildsUsers {
			if _, err = s.DB.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
				return err
			}
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			if rebuildsUsers {
				_, _ = s.DB.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
			}
			return err
		}
		if _, err = tx.ExecContext(ctx, string(body)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (?)`, e.Name())
		}
		if err != nil {
			tx.Rollback()
			if rebuildsUsers {
				_, _ = s.DB.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
			}
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		if err = tx.Commit(); err != nil {
			if rebuildsUsers {
				_, _ = s.DB.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
			}
			return err
		}
		if rebuildsUsers {
			if _, err = s.DB.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
				return err
			}
			var table string
			if err = s.DB.QueryRowContext(ctx, `SELECT "table" FROM pragma_foreign_key_check LIMIT 1`).Scan(&table); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("migration %s foreign-key check: %w", e.Name(), err)
			}
			if table != "" {
				return fmt.Errorf("migration %s left a foreign-key violation in %s", e.Name(), table)
			}
		}
	}
	return nil
}

// CurrentMigrationVersions returns the migrations shipped with this archive
// binary, in application order. Callers that only inspect an archive should
// use this list to check completeness without running migrations.
func CurrentMigrationVersions() []string {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		versions = append(versions, entry.Name())
	}
	return versions
}

func (s *Store) ListPosts(ctx context.Context, query string, page, perPage int) (PostPage, error) {
	return s.ListPostsSorted(ctx, query, SearchSortNewest, page, perPage)
}

func (s *Store) ListPostsSorted(ctx context.Context, query, sort string, page, perPage int) (PostPage, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 24
	}
	search, err := ParseSearchQuery(query)
	if err != nil {
		return PostPage{}, err
	}
	order, err := searchOrder(sort)
	if err != nil {
		return PostPage{}, err
	}
	where := `p.status='published' AND p.deleted_at IS NULL AND p.quarantined_at IS NULL`
	args := []any{}
	where, args = searchPredicates(where, args, search)
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
	rows, err := s.DB.QueryContext(ctx, `SELECT p.id,p.status,p.original_path,p.thumbnail_path,p.mime_type,p.width,p.height,p.byte_size,p.sha256,p.source,COALESCE(p.published_at,'') FROM posts p WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, qargs...)
	if err != nil {
		return PostPage{}, err
	}
	defer rows.Close()
	result := PostPage{Total: total, Page: page, PerPage: perPage, Pages: pages, Query: search.String()}
	for rows.Next() {
		var p Post
		if err = rows.Scan(&p.ID, &p.Status, &p.OriginalPath, &p.ThumbnailPath, &p.MIMEType, &p.Width, &p.Height, &p.ByteSize, &p.SHA256, &p.Source, &p.PublishedAt); err != nil {
			return PostPage{}, err
		}
		result.Posts = append(result.Posts, p)
	}
	return result, rows.Err()
}

func (s *Store) ListPostsForAdmin(ctx context.Context, filter AdminPostFilter) (PostPage, error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PerPage < 1 || filter.PerPage > 100 {
		filter.PerPage = 24
	}
	status := filter.Status
	if status == "" {
		status = "all"
	}
	where := "1=1"
	args := []any{}
	switch status {
	case "draft", "published":
		where += " AND p.status=? AND p.deleted_at IS NULL AND p.quarantined_at IS NULL"
		args = append(args, status)
	case "deleted":
		where += " AND p.deleted_at IS NOT NULL"
	case "quarantined":
		where += " AND p.deleted_at IS NULL AND p.quarantined_at IS NOT NULL"
	default:
		status = "all"
	}
	if filter.UploaderID > 0 {
		where += " AND p.uploader_id=?"
		args = append(args, filter.UploaderID)
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM posts p WHERE `+where, args...).Scan(&total); err != nil {
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
	rows, err := s.DB.QueryContext(ctx, `SELECT p.id,p.status,p.original_path,p.thumbnail_path,p.mime_type,p.width,p.height,p.byte_size,p.sha256,p.source,COALESCE(p.published_at,''),COALESCE(p.uploader_id,0),COALESCE(u.username,''),COALESCE(p.deleted_at,''),COALESCE(p.quarantined_at,''),COALESCE(p.quarantined_by,0),COALESCE(p.quarantine_reason,''),COALESCE(p.quarantine_previous_status,'') FROM posts p LEFT JOIN users u ON u.id=p.uploader_id WHERE `+where+` ORDER BY p.id DESC LIMIT ? OFFSET ?`, qargs...)
	if err != nil {
		return PostPage{}, err
	}
	defer rows.Close()
	result := PostPage{Total: total, Page: filter.Page, PerPage: filter.PerPage, Pages: pages, Query: status}
	for rows.Next() {
		var p Post
		if err = rows.Scan(&p.ID, &p.Status, &p.OriginalPath, &p.ThumbnailPath, &p.MIMEType, &p.Width, &p.Height, &p.ByteSize, &p.SHA256, &p.Source, &p.PublishedAt, &p.UploaderID, &p.Uploader, &p.DeletedAt, &p.QuarantinedAt, &p.QuarantinedBy, &p.QuarantineReason, &p.QuarantinePreviousStatus); err != nil {
			return PostPage{}, err
		}
		result.Posts = append(result.Posts, p)
	}
	return result, rows.Err()
}

func (s *Store) ListPostsForUploader(ctx context.Context, actor User, filter UploaderPostFilter) (PostPage, error) {
	current, err := s.User(ctx, actor.ID)
	if err != nil || !current.CanUpload() {
		return PostPage{}, ErrPermission
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PerPage < 1 || filter.PerPage > 100 {
		filter.PerPage = 24
	}
	status := filter.Status
	if status != "draft" && status != "published" {
		status = "all"
	}
	where := "p.uploader_id=? AND p.deleted_at IS NULL AND p.quarantined_at IS NULL"
	args := []any{current.ID}
	if status != "all" {
		where += " AND p.status=?"
		args = append(args, status)
	}
	var total int
	if err = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM posts p WHERE `+where, args...).Scan(&total); err != nil {
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
	rows, err := s.DB.QueryContext(ctx, `SELECT p.id,p.status,p.original_path,p.thumbnail_path,p.mime_type,p.original_filename,p.width,p.height,p.byte_size,p.sha256,p.source,COALESCE(p.published_at,''),p.uploader_id,COALESCE(u.username,'') FROM posts p LEFT JOIN users u ON u.id=p.uploader_id WHERE `+where+` ORDER BY p.id DESC LIMIT ? OFFSET ?`, qargs...)
	if err != nil {
		return PostPage{}, err
	}
	defer rows.Close()
	result := PostPage{Total: total, Page: filter.Page, PerPage: filter.PerPage, Pages: pages, Query: status}
	for rows.Next() {
		var p Post
		if err = rows.Scan(&p.ID, &p.Status, &p.OriginalPath, &p.ThumbnailPath, &p.MIMEType, &p.OriginalFilename, &p.Width, &p.Height, &p.ByteSize, &p.SHA256, &p.Source, &p.PublishedAt, &p.UploaderID, &p.Uploader); err != nil {
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
	search, err := ParseSearchQuery(filter.Query)
	if err != nil {
		return PostPage{}, err
	}
	from := "posts p"
	where := `p.status='published' AND p.deleted_at IS NULL AND p.quarantined_at IS NULL`
	args := []any{}
	order := "p.published_at DESC,p.id DESC"
	switch source {
	case "all":
	case "favorites":
		from += " JOIN favorites f ON f.post_id=p.id"
		where += " AND f.user_id=? AND EXISTS(SELECT 1 FROM users v WHERE v.id=? AND v.suspended_at IS NULL)"
		args = append(args, filter.ViewerID, filter.ViewerID)
		order = "f.created_at DESC,p.id DESC"
	case "pool":
		var poolID int64
		err := s.DB.QueryRowContext(ctx, `SELECT id FROM pools WHERE slug=? AND (status='published' OR (owner_id=? AND EXISTS(SELECT 1 FROM users v WHERE v.id=? AND v.suspended_at IS NULL)))`, filter.PoolSlug, filter.ViewerID, filter.ViewerID).Scan(&poolID)
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
	where, args = searchPredicates(where, args, search)
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
	result := PostPage{Total: total, Page: filter.Page, PerPage: filter.PerPage, Pages: pages, Query: search.String()}
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
	_ = canModerate
	var p Post
	var uploader sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT p.id,p.status,p.original_path,p.thumbnail_path,p.mime_type,p.original_filename,p.width,p.height,p.byte_size,p.sha256,p.source,COALESCE(p.published_at,''),p.uploader_id,COALESCE(u.username,'') FROM posts p LEFT JOIN users u ON u.id=p.uploader_id WHERE p.id=? AND p.deleted_at IS NULL AND p.quarantined_at IS NULL AND (p.status='published' OR (p.uploader_id=? AND EXISTS(SELECT 1 FROM users v WHERE v.id=? AND v.suspended_at IS NULL)) OR EXISTS(SELECT 1 FROM users v WHERE v.id=? AND v.suspended_at IS NULL AND v.role IN ('moderator','admin')))`, id, viewerID, viewerID, viewerID).Scan(&p.ID, &p.Status, &p.OriginalPath, &p.ThumbnailPath, &p.MIMEType, &p.OriginalFilename, &p.Width, &p.Height, &p.ByteSize, &p.SHA256, &p.Source, &p.PublishedAt, &uploader, &p.Uploader)
	if err != nil {
		return p, err
	}
	if uploader.Valid {
		p.UploaderID = uploader.Int64
	}
	if viewerID != 0 {
		_ = s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM favorites f JOIN users v ON v.id=f.user_id WHERE f.post_id=? AND f.user_id=? AND v.suspended_at IS NULL)`, id, viewerID).Scan(&p.Favorite)
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

func (s *Store) MediaVisibleToUser(ctx context.Context, kind, path string, viewerID int64, canModerate bool) (bool, error) {
	column := "original_path"
	switch kind {
	case "originals":
	case "thumbs":
		column = "thumbnail_path"
	default:
		return false, errors.New("invalid media kind")
	}
	_ = canModerate
	var visible int
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM posts p WHERE p.`+column+`=? AND p.deleted_at IS NULL AND ((p.quarantined_at IS NULL AND (p.status='published' OR (p.uploader_id=? AND EXISTS(SELECT 1 FROM users v WHERE v.id=? AND v.suspended_at IS NULL)) OR EXISTS(SELECT 1 FROM users v WHERE v.id=? AND v.suspended_at IS NULL AND v.role IN ('moderator','admin')))) OR (p.quarantined_at IS NOT NULL AND EXISTS(SELECT 1 FROM users v WHERE v.id=? AND v.suspended_at IS NULL AND v.is_super_admin=1))))`, path, viewerID, viewerID, viewerID, viewerID).Scan(&visible)
	return visible != 0, err
}

func (s *Store) Tags(ctx context.Context) ([]Tag, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT t.id,t.name,t.display_name,t.category,count(p.id) FROM tags t LEFT JOIN post_tags pt ON pt.tag_id=t.id LEFT JOIN posts p ON p.id=pt.post_id AND p.status='published' AND p.deleted_at IS NULL AND p.quarantined_at IS NULL GROUP BY t.id HAVING count(p.id)>0 ORDER BY t.category,count(p.id) DESC,t.name LIMIT 100`)
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

func (s *Store) TagSuggestions(ctx context.Context, actor User, query string) ([]Tag, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.CanUpload() {
		return nil, ErrPermission
	}
	needle, category := tagSuggestionNeedle(query)
	if needle == "" {
		return []Tag{}, nil
	}
	sqlQuery := `SELECT id,name,display_name,category FROM tags WHERE name LIKE ? ESCAPE '\'`
	args := []any{escapeLike(needle) + "%"}
	if category != "" {
		sqlQuery += ` AND category=?`
		args = append(args, category)
	}
	sqlQuery += ` ORDER BY CASE WHEN name=? THEN 0 ELSE 1 END,name LIMIT ?`
	args = append(args, needle, tagSuggestionLimit)
	rows, err := tx.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tag
	for rows.Next() {
		var tag Tag
		if err = rows.Scan(&tag.ID, &tag.Name, &tag.DisplayName, &tag.Category); err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

// PublicTagSuggestions returns tags that are attached to at least one visible published post.
func (s *Store) PublicTagSuggestions(ctx context.Context, query string) ([]Tag, error) {
	if len(query) > maxSearchQueryLength {
		return []Tag{}, nil
	}
	needle, category := publicTagSuggestionNeedle(query)
	if needle == "" {
		return []Tag{}, nil
	}
	sqlQuery := `SELECT t.id,t.name,t.display_name,t.category FROM tags t WHERE t.name LIKE ? ESCAPE '\' AND EXISTS (
		SELECT 1 FROM post_tags pt JOIN posts p ON p.id=pt.post_id
		WHERE pt.tag_id=t.id AND p.status='published' AND p.deleted_at IS NULL AND p.quarantined_at IS NULL
	)`
	args := []any{escapeLike(needle) + "%"}
	if category != "" {
		sqlQuery += ` AND t.category=?`
		args = append(args, category)
	}
	sqlQuery += ` ORDER BY CASE WHEN t.name=? THEN 0 ELSE 1 END,t.name LIMIT ?`
	args = append(args, needle, tagSuggestionLimit)
	rows, err := s.DB.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tag
	for rows.Next() {
		var tag Tag
		if err = rows.Scan(&tag.ID, &tag.Name, &tag.DisplayName, &tag.Category); err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

func publicTagSuggestionNeedle(query string) (string, string) {
	fields := strings.Fields(strings.ToLower(query))
	if len(fields) == 0 {
		return "", ""
	}
	token := strings.TrimPrefix(fields[len(fields)-1], "-")
	category, token, explicit := tagCategoryPrefix(token)
	token = strings.Trim(token, "#, ")
	token = slugCleanup.ReplaceAllString(token, "_")
	if !explicit {
		category = ""
	}
	return strings.TrimLeft(token, "_"), category
}

func tagSuggestionNeedle(query string) (string, string) {
	fields := strings.Fields(strings.ToLower(query))
	if len(fields) == 0 {
		return "", ""
	}
	category, token, explicit := tagCategoryPrefix(fields[len(fields)-1])
	token = strings.Trim(token, "#, ")
	token = slugCleanup.ReplaceAllString(token, "_")
	if !explicit {
		category = ""
	}
	return strings.TrimLeft(token, "_"), category
}

func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func (s *Store) Pools(ctx context.Context) ([]Pool, error) {
	return s.PoolsForUser(ctx, 0)
}
func (s *Store) PoolsForUser(ctx context.Context, viewerID int64) ([]Pool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT po.id,po.slug,po.name,po.description,po.status,COALESCE(po.owner_id,0),COALESCE(u.username,''),count(CASE WHEN p.status='published' AND p.deleted_at IS NULL AND p.quarantined_at IS NULL THEN 1 END) FROM pools po LEFT JOIN users u ON u.id=po.owner_id LEFT JOIN pool_posts pp ON pp.pool_id=po.id LEFT JOIN posts p ON p.id=pp.post_id WHERE po.status='published' OR (po.owner_id=? AND EXISTS(SELECT 1 FROM users v WHERE v.id=? AND v.suspended_at IS NULL)) GROUP BY po.id ORDER BY po.status,po.name`, viewerID, viewerID)
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
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM posts WHERE status='published' AND deleted_at IS NULL AND quarantined_at IS NULL ORDER BY random() LIMIT 1`).Scan(&id)
	return id, err
}
