package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const maxTagInventoryPerPage = 24

var (
	ErrTagMaintenanceConflict         = errors.New("tag maintenance conflict")
	ErrTagMaintenanceCategoryConflict = errors.New("tag maintenance category conflict")
	ErrTagMaintenanceStale            = errors.New("tag maintenance preview is stale")
	ErrTagMaintenanceNoOp             = errors.New("tag maintenance operation is a no-op")
)

type TagInventoryFilter struct {
	Query, Category string
	Page, PerPage   int
}

type TagPage struct {
	Tags                 []Tag
	Total, Page, PerPage int
	Pages                int
	Query, Category      string
}

type TagMaintenanceRequest struct {
	Operation      string
	Source, Target string
	Reason         string
}

type TagMaintenancePreview struct {
	Operation         string
	Source, Target    Tag
	AffectedPostCount int
	Conflicts         []string
	NoOp              bool
	Fingerprint       string
	Reason            string
}

func (s *Store) ListTagInventory(ctx context.Context, actor User, filter TagInventoryFilter) (TagPage, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return TagPage{}, err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.IsSuperAdmin {
		return TagPage{}, ErrPermission
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PerPage < 1 || filter.PerPage > maxTagInventoryPerPage {
		filter.PerPage = maxTagInventoryPerPage
	}
	category := filter.Category
	if !validTagCategory(category) {
		category = ""
	}
	query := strings.TrimSpace(strings.ToLower(filter.Query))
	if len([]rune(query)) > 80 {
		query = string([]rune(query)[:80])
	}
	where := "1=1"
	args := []any{}
	if query != "" {
		where += ` AND (t.name LIKE ? ESCAPE '\' OR t.display_name LIKE ? ESCAPE '\')`
		needle := "%" + escapeLike(query) + "%"
		args = append(args, needle, needle)
	}
	if category != "" {
		where += " AND t.category=?"
		args = append(args, category)
	}
	var total int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM tags t WHERE `+where, args...).Scan(&total); err != nil {
		return TagPage{}, err
	}
	pages := (total + filter.PerPage - 1) / filter.PerPage
	if pages == 0 {
		pages = 1
	}
	if filter.Page > pages {
		filter.Page = pages
	}
	qargs := append(append([]any{}, args...), filter.PerPage, (filter.Page-1)*filter.PerPage)
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.name,t.display_name,t.category,COUNT(p.id) FROM tags t LEFT JOIN post_tags pt ON pt.tag_id=t.id LEFT JOIN posts p ON p.id=pt.post_id WHERE `+where+` GROUP BY t.id ORDER BY t.category,t.name LIMIT ? OFFSET ?`, qargs...)
	if err != nil {
		return TagPage{}, err
	}
	defer rows.Close()
	page := TagPage{Total: total, Page: filter.Page, PerPage: filter.PerPage, Pages: pages, Query: query, Category: category}
	for rows.Next() {
		var tag Tag
		if err = rows.Scan(&tag.ID, &tag.Name, &tag.DisplayName, &tag.Category, &tag.Count); err != nil {
			return TagPage{}, err
		}
		page.Tags = append(page.Tags, tag)
	}
	if err = rows.Err(); err != nil {
		return TagPage{}, err
	}
	return page, tx.Commit()
}

func (s *Store) PreviewTagMaintenance(ctx context.Context, actor User, request TagMaintenanceRequest) (TagMaintenancePreview, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return TagMaintenancePreview{}, err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.IsSuperAdmin {
		return TagMaintenancePreview{}, ErrPermission
	}
	preview, err := previewTagMaintenanceTx(ctx, tx, request)
	if err != nil && preview.Fingerprint == "" {
		return preview, err
	}
	return preview, nil
}

func (s *Store) ApplyTagMaintenance(ctx context.Context, actor User, request TagMaintenanceRequest, expected TagMaintenancePreview) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.IsSuperAdmin {
		return ErrPermission
	}
	preview, previewErr := previewTagMaintenanceTx(ctx, tx, request)
	if previewErr != nil {
		return previewErr
	}
	if expected.Fingerprint == "" || expected.Fingerprint != preview.Fingerprint || expected.Operation != preview.Operation || normalizeTagName(expected.Source.Name) != preview.Source.Name || normalizeTagName(expected.Target.Name) != preview.Target.Name {
		return ErrTagMaintenanceStale
	}
	if preview.NoOp {
		return ErrTagMaintenanceNoOp
	}
	switch preview.Operation {
	case "rename":
		if _, err = tx.ExecContext(ctx, `UPDATE tags SET name=?,display_name=? WHERE id=?`, preview.Target.Name, strings.ReplaceAll(preview.Target.Name, "_", " "), preview.Source.ID); err != nil {
			return err
		}
		if err = insertAuditRecordTx(ctx, tx, current, "tag_rename", 0, "", 0, tagMaintenanceReason("rename", preview, request.Reason), "", "", 0); err != nil {
			return err
		}
	case "merge":
		if _, err = tx.ExecContext(ctx, `INSERT INTO post_tags(post_id,tag_id,assigned_at) SELECT post_id,?,assigned_at FROM post_tags WHERE tag_id=? ON CONFLICT(post_id,tag_id) DO NOTHING`, preview.Target.ID, preview.Source.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM post_tags WHERE tag_id=?`, preview.Source.ID); err != nil {
			return err
		}
		var remaining int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM post_tags WHERE tag_id=?`, preview.Source.ID).Scan(&remaining); err != nil {
			return err
		}
		if remaining != 0 {
			return errors.New("source tag relationships remain after merge")
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM tags WHERE id=?`, preview.Source.ID); err != nil {
			return err
		}
		if err = insertAuditRecordTx(ctx, tx, current, "tag_merge", 0, "", 0, tagMaintenanceReason("merge", preview, request.Reason), "", "", 0); err != nil {
			return err
		}
	default:
		return ErrTagMaintenanceConflict
	}
	return tx.Commit()
}

func previewTagMaintenanceTx(ctx context.Context, tx *sql.Tx, request TagMaintenanceRequest) (TagMaintenancePreview, error) {
	operation := strings.ToLower(strings.TrimSpace(request.Operation))
	sourceName := normalizeTagName(request.Source)
	targetName := normalizeTagName(request.Target)
	if operation != "rename" && operation != "merge" || sourceName == "" || targetName == "" || len([]rune(sourceName)) > 80 || len([]rune(targetName)) > 80 {
		return TagMaintenancePreview{Operation: operation}, ErrTagMaintenanceConflict
	}
	var source Tag
	err := tx.QueryRowContext(ctx, `SELECT id,name,display_name,category FROM tags WHERE name=?`, sourceName).Scan(&source.ID, &source.Name, &source.DisplayName, &source.Category)
	if err != nil {
		return TagMaintenancePreview{}, err
	}
	var target Tag
	targetErr := tx.QueryRowContext(ctx, `SELECT id,name,display_name,category FROM tags WHERE name=?`, targetName).Scan(&target.ID, &target.Name, &target.DisplayName, &target.Category)
	if targetErr != nil && !errors.Is(targetErr, sql.ErrNoRows) {
		return TagMaintenancePreview{}, targetErr
	}
	preview := TagMaintenancePreview{Operation: operation, Source: source, Target: target, Reason: request.Reason}
	preview.Source.Count, err = tagRelationCount(ctx, tx, source.ID)
	if err != nil {
		return TagMaintenancePreview{}, err
	}
	preview.AffectedPostCount = preview.Source.Count
	if sourceName == targetName {
		preview.Target = source
		preview.NoOp = true
		preview.Fingerprint, err = tagMaintenanceFingerprint(ctx, tx, operation, source, source)
		return preview, err
	}
	if operation == "rename" && target.ID != 0 {
		preview.Conflicts = []string{fmt.Sprintf("target tag %q already exists", target.Name)}
		return preview, ErrTagMaintenanceConflict
	}
	if operation == "rename" {
		preview.Target = Tag{Name: targetName, DisplayName: strings.ReplaceAll(targetName, "_", " "), Category: source.Category}
	}
	if operation == "merge" {
		if target.ID == 0 {
			preview.Conflicts = []string{fmt.Sprintf("target tag %q does not exist", targetName)}
			return preview, sql.ErrNoRows
		}
		if source.Category != target.Category {
			preview.Conflicts = []string{fmt.Sprintf("source category %q differs from target category %q", source.Category, target.Category)}
			return preview, ErrTagMaintenanceCategoryConflict
		}
	}
	preview.Fingerprint, err = tagMaintenanceFingerprint(ctx, tx, operation, source, target)
	if err != nil {
		return TagMaintenancePreview{}, err
	}
	return preview, nil
}

func tagRelationCount(ctx context.Context, tx *sql.Tx, tagID int64) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(DISTINCT pt.post_id) FROM post_tags pt JOIN posts p ON p.id=pt.post_id WHERE pt.tag_id=?`, tagID).Scan(&count)
	return count, err
}

func tagMaintenanceFingerprint(ctx context.Context, tx *sql.Tx, operation string, source, target Tag) (string, error) {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s|source:%d:%s:%s:%s|target:%d:%s:%s:%s|", operation, source.ID, source.Name, source.DisplayName, source.Category, target.ID, target.Name, target.DisplayName, target.Category)
	for _, tagID := range []int64{source.ID, target.ID} {
		if tagID == 0 {
			continue
		}
		rows, err := tx.QueryContext(ctx, `SELECT pt.post_id FROM post_tags pt JOIN posts p ON p.id=pt.post_id WHERE pt.tag_id=? ORDER BY pt.post_id`, tagID)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var postID int64
			if err = rows.Scan(&postID); err != nil {
				rows.Close()
				return "", err
			}
			_, _ = fmt.Fprintf(hash, "%d,", postID)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return "", err
		}
		rows.Close()
		_, _ = hash.Write([]byte("|"))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func tagMaintenanceReason(operation string, preview TagMaintenancePreview, reason string) string {
	prefix := fmt.Sprintf("tag %s: %s [%s] -> %s [%s]; affected posts=%d; reason=", operation, preview.Source.Name, preview.Source.Category, preview.Target.Name, preview.Target.Category, preview.AffectedPostCount)
	available := maxAuditReasonLength - len([]rune(prefix))
	if available < 0 {
		available = 0
	}
	clean := []rune(strings.TrimSpace(reason))
	if len(clean) > available {
		clean = clean[:available]
	}
	return prefix + string(clean)
}
