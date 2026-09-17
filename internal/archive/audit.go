package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

const (
	maxAuditEventTypeLength = 64
	maxAuditReasonLength    = 500
	maxAuditSnapshotBytes   = 32768
	maxAuditPathLength      = 4096
	maxAuditMIMETypeLength  = 128
	maxAuditFilenameLength  = 255
	maxAuditSHA256Length    = 128
	maxAuditSourceLength    = 4096
	maxAuditTags            = 512
)

var (
	ErrAuditConflict      = errors.New("audit event no longer matches the current post")
	ErrAuditNotRevertable = errors.New("audit event cannot be reverted")
	ErrAuditSnapshot      = errors.New("invalid audit snapshot")
)

type auditTagSnapshot struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

type auditSnapshot struct {
	Source               string             `json:"source"`
	Status               string             `json:"status"`
	PublishedAt          string             `json:"published_at"`
	OriginalPath         string             `json:"original_path,omitempty"`
	ThumbnailPath        string             `json:"thumbnail_path,omitempty"`
	MIMEType             string             `json:"mime_type,omitempty"`
	OriginalFilename     string             `json:"original_filename,omitempty"`
	Width                int                `json:"width,omitempty"`
	Height               int                `json:"height,omitempty"`
	ByteSize             int64              `json:"byte_size,omitempty"`
	SHA256               string             `json:"sha256,omitempty"`
	DeletedAt            string             `json:"deleted_at,omitempty"`
	QuarantinedAt        string             `json:"quarantined_at,omitempty"`
	QuarantineReason     string             `json:"quarantine_reason,omitempty"`
	QuarantinePrevStatus string             `json:"quarantine_previous_status,omitempty"`
	Tags                 []auditTagSnapshot `json:"tags"`
}

type AuditEvent struct {
	ID              int64
	EventType       string
	ActorID         int64
	Actor           string
	UploaderID      int64
	Uploader        string
	PostID          int64
	FromRole        string
	ToRole          string
	Reason          string
	BeforeSnapshot  string
	AfterSnapshot   string
	RevertedEventID int64
	CreatedAt       string
}

type AuditFilter struct {
	EventType string
	ActorID   int64
	PostID    int64
	Page      int
	PerPage   int
}

type AuditPage struct {
	Events               []AuditEvent
	Total, Page, PerPage int
	Pages                int
	EventType            string
	ActorID, PostID      int64
}

var auditEventTypes = []string{
	"metadata_change",
	"bulk_tag_change",
	"quarantine",
	"restore",
	"role_change",
	"role_change_quarantine",
	"suspension_change",
	"super_admin_transfer",
	"permanent_delete",
	"revert",
}

func AuditEventTypes() []string {
	return append([]string(nil), auditEventTypes...)
}

func auditReason(raw, fallback string) string {
	reason := strings.TrimSpace(raw)
	if reason == "" {
		reason = fallback
	}
	if len([]rune(reason)) > maxAuditReasonLength {
		reason = string([]rune(reason)[:maxAuditReasonLength])
	}
	return reason
}

func auditSnapshotFromPost(post Post) auditSnapshot {
	snapshot := auditSnapshot{Source: post.Source, Status: post.Status, PublishedAt: post.PublishedAt}
	for _, tag := range post.Tags {
		snapshot.Tags = append(snapshot.Tags, auditTagSnapshot{Name: tag.Name, Category: tag.Category})
	}
	sort.Slice(snapshot.Tags, func(i, j int) bool {
		if snapshot.Tags[i].Category == snapshot.Tags[j].Category {
			return snapshot.Tags[i].Name < snapshot.Tags[j].Name
		}
		return snapshot.Tags[i].Category < snapshot.Tags[j].Category
	})
	return snapshot
}

func auditFinalSnapshotFromPost(post Post) auditSnapshot {
	snapshot := auditSnapshotFromPost(post)
	snapshot.OriginalPath = post.OriginalPath
	snapshot.ThumbnailPath = post.ThumbnailPath
	snapshot.MIMEType = post.MIMEType
	snapshot.OriginalFilename = post.OriginalFilename
	snapshot.Width = post.Width
	snapshot.Height = post.Height
	snapshot.ByteSize = post.ByteSize
	snapshot.SHA256 = post.SHA256
	snapshot.DeletedAt = post.DeletedAt
	snapshot.QuarantinedAt = post.QuarantinedAt
	snapshot.QuarantineReason = post.QuarantineReason
	snapshot.QuarantinePrevStatus = post.QuarantinePreviousStatus
	return snapshot
}

func encodeAuditSnapshot(snapshot auditSnapshot) (string, error) {
	if err := validateAuditSnapshotValue(snapshot); err != nil {
		return "", err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrAuditSnapshot, err)
	}
	if len(data) > maxAuditSnapshotBytes {
		return "", fmt.Errorf("%w: snapshot exceeds %d bytes", ErrAuditSnapshot, maxAuditSnapshotBytes)
	}
	return string(data), nil
}

func decodeAuditSnapshot(raw string) (auditSnapshot, error) {
	if raw == "" || len(raw) > maxAuditSnapshotBytes {
		return auditSnapshot{}, ErrAuditSnapshot
	}
	var snapshot auditSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return auditSnapshot{}, fmt.Errorf("%w: %v", ErrAuditSnapshot, err)
	}
	if err := validateAuditSnapshotValue(snapshot); err != nil {
		return auditSnapshot{}, err
	}
	return snapshot, nil
}

func formatAuditSnapshot(raw string) (string, error) {
	snapshot, err := decodeAuditSnapshot(raw)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrAuditSnapshot, err)
	}
	return string(data), nil
}

func validateAuditSnapshotValue(snapshot auditSnapshot) error {
	if snapshot.Status != "draft" && snapshot.Status != "published" {
		return fmt.Errorf("%w: invalid status", ErrAuditSnapshot)
	}
	if len([]rune(snapshot.Source)) > maxAuditSourceLength || len(snapshot.PublishedAt) > 64 || len(snapshot.Tags) > maxAuditTags {
		return fmt.Errorf("%w: snapshot field exceeds limit", ErrAuditSnapshot)
	}
	if len([]rune(snapshot.OriginalPath)) > maxAuditPathLength || len([]rune(snapshot.ThumbnailPath)) > maxAuditPathLength || len([]rune(snapshot.MIMEType)) > maxAuditMIMETypeLength || len([]rune(snapshot.OriginalFilename)) > maxAuditFilenameLength || len([]rune(snapshot.SHA256)) > maxAuditSHA256Length || len([]rune(snapshot.DeletedAt)) > 64 || len([]rune(snapshot.QuarantinedAt)) > 64 || len([]rune(snapshot.QuarantineReason)) > maxAuditReasonLength || len([]rune(snapshot.QuarantinePrevStatus)) > 16 || snapshot.Width < 0 || snapshot.Height < 0 || snapshot.ByteSize < 0 {
		return fmt.Errorf("%w: snapshot field exceeds limit", ErrAuditSnapshot)
	}
	if snapshot.PublishedAt != "" {
		if _, err := time.Parse(time.RFC3339, snapshot.PublishedAt); err != nil {
			return fmt.Errorf("%w: invalid published timestamp", ErrAuditSnapshot)
		}
	}
	seen := make(map[string]struct{}, len(snapshot.Tags))
	for _, tag := range snapshot.Tags {
		if tag.Name == "" || normalizeTagName(tag.Name) != tag.Name || len([]rune(tag.Name)) > 80 || !validTagCategory(tag.Category) {
			return fmt.Errorf("%w: invalid tag", ErrAuditSnapshot)
		}
		if _, exists := seen[tag.Name]; exists {
			return fmt.Errorf("%w: duplicate tag", ErrAuditSnapshot)
		}
		seen[tag.Name] = struct{}{}
	}
	return nil
}

func validTagCategory(category string) bool {
	switch category {
	case "artist", "character", "copyright", "general", "meta":
		return true
	default:
		return false
	}
}

func loadAuditPost(ctx context.Context, tx *sql.Tx, id int64) (Post, error) {
	post, err := scanPost(tx.QueryRowContext(ctx, postReviewSelect+` WHERE p.id=? AND p.deleted_at IS NULL AND p.quarantined_at IS NULL`, id))
	if err != nil {
		return Post{}, err
	}
	if err = loadPostTagsTx(ctx, tx, &post); err != nil {
		return Post{}, err
	}
	return post, nil
}

func loadPostTagsTx(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, post *Post) error {
	rows, err := queryer.QueryContext(ctx, `SELECT t.id,t.name,t.display_name,t.category FROM tags t JOIN post_tags pt ON pt.tag_id=t.id WHERE pt.post_id=? ORDER BY t.category,t.name`, post.ID)
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

func insertAuditRecordTx(ctx context.Context, tx *sql.Tx, actor User, eventType string, targetUserID int64, uploader string, postID int64, reason, before, after string, revertedEventID int64) error {
	return insertAuditRecordWithRolesTx(ctx, tx, actor, eventType, targetUserID, uploader, postID, "", "", reason, before, after, revertedEventID)
}

func insertAuditRecordWithRolesTx(ctx context.Context, tx *sql.Tx, actor User, eventType string, targetUserID int64, uploader string, postID int64, fromRole, toRole, reason, before, after string, revertedEventID int64) error {
	if len(eventType) == 0 || len(eventType) > maxAuditEventTypeLength || len([]rune(reason)) > maxAuditReasonLength {
		return ErrAuditSnapshot
	}
	if before != "" {
		if _, err := decodeAuditSnapshot(before); err != nil {
			return err
		}
	}
	if after != "" {
		if _, err := decodeAuditSnapshot(after); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_events(event_type,actor_id,actor_username,target_user_id,uploader_username,post_id,from_role,to_role,reason,before_snapshot,after_snapshot,reverted_event_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, eventType, actor.ID, actor.Username, nullInt64(targetUserID), uploader, nullInt64(postID), nullString(fromRole), nullString(toRole), auditReason(reason, eventType), before, after, nullInt64(revertedEventID), time.Now().UTC().Format(time.RFC3339))
	return err
}

func nullInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func insertPostAuditTx(ctx context.Context, tx *sql.Tx, actor User, eventType string, post Post, reason string, before, after auditSnapshot, revertedEventID int64) error {
	beforeJSON, err := encodeAuditSnapshot(before)
	if err != nil {
		return err
	}
	afterJSON, err := encodeAuditSnapshot(after)
	if err != nil {
		return err
	}
	if beforeJSON == afterJSON {
		return nil
	}
	return insertAuditRecordTx(ctx, tx, actor, eventType, post.UploaderID, post.Uploader, post.ID, reason, beforeJSON, afterJSON, revertedEventID)
}

func auditSnapshotEqual(left, right auditSnapshot) bool {
	sort.Slice(left.Tags, func(i, j int) bool {
		if left.Tags[i].Category == left.Tags[j].Category {
			return left.Tags[i].Name < left.Tags[j].Name
		}
		return left.Tags[i].Category < left.Tags[j].Category
	})
	sort.Slice(right.Tags, func(i, j int) bool {
		if right.Tags[i].Category == right.Tags[j].Category {
			return right.Tags[i].Name < right.Tags[j].Name
		}
		return right.Tags[i].Category < right.Tags[j].Category
	})
	return reflect.DeepEqual(left, right)
}

func (s *Store) ListAuditEvents(ctx context.Context, actor User, filter AuditFilter) (AuditPage, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return AuditPage{}, err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.IsSuperAdmin {
		return AuditPage{}, ErrPermission
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PerPage < 1 || filter.PerPage > 100 {
		filter.PerPage = 24
	}
	where := "1=1"
	args := []any{}
	if filter.EventType != "" {
		where += " AND ae.event_type=?"
		args = append(args, filter.EventType)
	}
	if filter.ActorID > 0 {
		where += " AND ae.actor_id=?"
		args = append(args, filter.ActorID)
	}
	if filter.PostID > 0 {
		where += " AND ae.post_id=?"
		args = append(args, filter.PostID)
	}
	var total int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM audit_events ae WHERE `+where, args...).Scan(&total); err != nil {
		return AuditPage{}, err
	}
	pages := (total + filter.PerPage - 1) / filter.PerPage
	if pages == 0 {
		pages = 1
	}
	if filter.Page > pages {
		filter.Page = pages
	}
	qargs := append(append([]any{}, args...), filter.PerPage, (filter.Page-1)*filter.PerPage)
	rows, err := tx.QueryContext(ctx, `SELECT ae.id,ae.event_type,COALESCE(ae.actor_id,0),CASE WHEN ae.actor_username<>'' THEN ae.actor_username ELSE COALESCE(au.username,'') END,COALESCE(ae.target_user_id,0),CASE WHEN ae.uploader_username<>'' THEN ae.uploader_username ELSE COALESCE(tu.username,'') END,COALESCE(ae.post_id,0),COALESCE(ae.from_role,''),COALESCE(ae.to_role,''),ae.reason,ae.before_snapshot,ae.after_snapshot,COALESCE(ae.reverted_event_id,0),ae.created_at FROM audit_events ae LEFT JOIN users au ON au.id=ae.actor_id LEFT JOIN users tu ON tu.id=ae.target_user_id WHERE `+where+` ORDER BY ae.id DESC LIMIT ? OFFSET ?`, qargs...)
	if err != nil {
		return AuditPage{}, err
	}
	defer rows.Close()
	page := AuditPage{Total: total, Page: filter.Page, PerPage: filter.PerPage, Pages: pages, EventType: filter.EventType, ActorID: filter.ActorID, PostID: filter.PostID}
	for rows.Next() {
		var event AuditEvent
		if err = rows.Scan(&event.ID, &event.EventType, &event.ActorID, &event.Actor, &event.UploaderID, &event.Uploader, &event.PostID, &event.FromRole, &event.ToRole, &event.Reason, &event.BeforeSnapshot, &event.AfterSnapshot, &event.RevertedEventID, &event.CreatedAt); err != nil {
			return AuditPage{}, err
		}
		if event.BeforeSnapshot != "" {
			if event.BeforeSnapshot, err = formatAuditSnapshot(event.BeforeSnapshot); err != nil {
				return AuditPage{}, err
			}
		}
		if event.AfterSnapshot != "" {
			if event.AfterSnapshot, err = formatAuditSnapshot(event.AfterSnapshot); err != nil {
				return AuditPage{}, err
			}
		}
		page.Events = append(page.Events, event)
	}
	if err = rows.Err(); err != nil {
		return AuditPage{}, err
	}
	return page, tx.Commit()
}

func (s *Store) RevertAuditEvent(ctx context.Context, actor User, eventID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.actorTx(ctx, tx, actor)
	if err != nil || !current.IsSuperAdmin {
		return ErrPermission
	}
	var eventType, uploader string
	var postID, targetUserID int64
	var beforeJSON, afterJSON string
	if err = tx.QueryRowContext(ctx, `SELECT event_type,COALESCE(post_id,0),COALESCE(target_user_id,0),uploader_username,before_snapshot,after_snapshot FROM audit_events WHERE id=?`, eventID).Scan(&eventType, &postID, &targetUserID, &uploader, &beforeJSON, &afterJSON); err != nil {
		return err
	}
	if eventType != "metadata_change" && eventType != "bulk_tag_change" || postID <= 0 || beforeJSON == "" || afterJSON == "" {
		return ErrAuditNotRevertable
	}
	before, err := decodeAuditSnapshot(beforeJSON)
	if err != nil {
		return err
	}
	after, err := decodeAuditSnapshot(afterJSON)
	if err != nil {
		return err
	}
	post, err := loadAuditPost(ctx, tx, postID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAuditNotRevertable
	}
	if err != nil {
		return err
	}
	currentSnapshot := auditSnapshotFromPost(post)
	if !auditSnapshotEqual(currentSnapshot, after) {
		return ErrAuditConflict
	}
	parsedTags := make([]parsedTag, 0, len(before.Tags))
	for _, tag := range before.Tags {
		parsedTags = append(parsedTags, parsedTag{Name: tag.Name, Category: tag.Category, Explicit: true})
	}
	if err = validateTagCategories(ctx, tx, parsedTags); err != nil {
		return err
	}
	if err = ensureTags(ctx, tx, parsedTags); err != nil {
		return err
	}
	publishedAt := any(nil)
	if before.PublishedAt != "" {
		publishedAt = before.PublishedAt
	}
	if _, err = tx.ExecContext(ctx, `UPDATE posts SET source=?,status=?,published_at=? WHERE id=? AND deleted_at IS NULL AND quarantined_at IS NULL`, before.Source, before.Status, publishedAt, postID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM post_tags WHERE post_id=?`, postID); err != nil {
		return err
	}
	for _, tag := range parsedTags {
		if _, err = tx.ExecContext(ctx, `INSERT INTO post_tags(post_id,tag_id) SELECT ?,id FROM tags WHERE name=?`, postID, tag.Name); err != nil {
			return err
		}
	}
	if post.Uploader == "" {
		post.Uploader = uploader
	}
	post.UploaderID = targetUserID
	if err = insertPostAuditTx(ctx, tx, current, "revert", post, fmt.Sprintf("reverted audit event #%d", eventID), after, before, eventID); err != nil {
		return err
	}
	return tx.Commit()
}
