package main

import (
	"archive/zip"
	"context"
	"database/sql"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kura/internal/archive"
)

type maintenanceFixture struct {
	dbPath, mediaRoot string
	counts            map[string]int
	media             map[string][]byte
}

func TestBackupVerifyRestoreRoundTripPreservesArchiveStateAndMedia(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	archivePath := filepath.Join(t.TempDir(), "kura-backup.zip")
	if err := run([]string{"backup", "-db", fixture.dbPath, "-media", fixture.mediaRoot, "-out", archivePath}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "-in", archivePath}); err != nil {
		t.Fatal(err)
	}
	restoredDB := filepath.Join(t.TempDir(), "restored", "kura.db")
	restoredMedia := filepath.Join(filepath.Dir(restoredDB), "media")
	if err := os.MkdirAll(filepath.Dir(restoredDB), 0700); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"restore", "-in", archivePath, "-db", restoredDB, "-media", restoredMedia}); err != nil {
		t.Fatal(err)
	}

	store, err := archive.Open(restoredDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for query, want := range fixture.counts {
		var got int
		if err := store.DB.QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("count %q: %v", query, err)
		}
		if got != want {
			t.Fatalf("count %q = %d, want %d", query, got, want)
		}
	}
	for rel, want := range fixture.media {
		got, err := os.ReadFile(filepath.Join(restoredMedia, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("restored media %s: %v", rel, err)
		}
		if string(got) != string(want) {
			t.Fatalf("restored media %s changed", rel)
		}
	}
}

func TestDocumentedCLIBackupVerifyRestoreDrill(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	root := t.TempDir()
	archivePath := filepath.Join(root, "kura-backup.zip")
	restoredDB := filepath.Join(root, "restored", "kura.db")
	restoredMedia := filepath.Join(root, "restored", "media")
	if err := os.MkdirAll(filepath.Dir(restoredDB), 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"backup", "-db", fixture.dbPath, "-media", fixture.mediaRoot, "-out", archivePath},
		{"verify", "-in", archivePath},
		{"restore", "-in", archivePath, "-db", restoredDB, "-media", restoredMedia},
	} {
		cmd := exec.Command("go", append([]string{"run", "."}, args...)...)
		cmd.Dir = "."
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go run %v: %v\n%s", args, err, output)
		}
	}
	t.Logf("drill archive=%s restored_db=%s restored_media=%s", archivePath, restoredDB, restoredMedia)
}

func TestBackupIncludesCommittedWALData(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	walDB, err := sql.Open("sqlite3", fixture.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer walDB.Close()
	if _, err = walDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	if _, err = walDB.Exec(`INSERT INTO tags(name,display_name,category) VALUES('wal-backed','WAL-backed','meta')`); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "wal.zip")
	if err = run([]string{"backup", "-db", fixture.dbPath, "-media", fixture.mediaRoot, "-out", archivePath}); err != nil {
		t.Fatal(err)
	}
	restoredDB := filepath.Join(t.TempDir(), "wal-restored.db")
	restoredMedia := filepath.Join(t.TempDir(), "media")
	if err = run([]string{"restore", "-in", archivePath, "-db", restoredDB, "-media", restoredMedia}); err != nil {
		t.Fatal(err)
	}
	store, err := archive.Open(restoredDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count int
	if err = store.DB.QueryRow(`SELECT count(*) FROM tags WHERE name='wal-backed'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("WAL-backed committed tag count=%d", count)
	}
}

func TestBackupRejectsDatabaseMissingCurrentMigration(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	db, err := sql.Open("sqlite3", fixture.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM schema_migrations WHERE version='006_audit_snapshots.sql'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(t.TempDir(), "missing-migration.zip")
	err = run([]string{"backup", "-db", fixture.dbPath, "-media", fixture.mediaRoot, "-out", archivePath})
	if err == nil || !strings.Contains(err.Error(), "schema migration 006") {
		t.Fatalf("backup error=%v, want missing current migration failure", err)
	}
}

func TestVerifyRejectsCorruptChecksum(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if err := run([]string{"backup", "-db", fixture.dbPath, "-media", fixture.mediaRoot, "-out", archivePath}); err != nil {
		t.Fatal(err)
	}
	rewriteZip(t, archivePath, func(name string, body []byte) []byte {
		if strings.HasPrefix(name, "media/") {
			body = append([]byte(nil), body...)
			body[0] ^= 0xff
		}
		return body
	})
	if err := run([]string{"verify", "-in", archivePath}); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("verify error=%v, want checksum failure", err)
	}
}

func TestVerifyRejectsTraversalEntry(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if err := run([]string{"backup", "-db", fixture.dbPath, "-media", fixture.mediaRoot, "-out", archivePath}); err != nil {
		t.Fatal(err)
	}
	addZipEntry(t, archivePath, "media/../escape", []byte("unsafe"))
	if err := run([]string{"verify", "-in", archivePath}); err == nil || !strings.Contains(err.Error(), "unsafe archive path") {
		t.Fatalf("verify error=%v, want traversal failure", err)
	}
}

func TestVerifyRejectsDuplicateEntry(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if err := run([]string{"backup", "-db", fixture.dbPath, "-media", fixture.mediaRoot, "-out", archivePath}); err != nil {
		t.Fatal(err)
	}
	addZipEntry(t, archivePath, "manifest.json", []byte("duplicate"))
	if err := run([]string{"verify", "-in", archivePath}); err == nil || !strings.Contains(err.Error(), "duplicate archive entry") {
		t.Fatalf("verify error=%v, want duplicate-entry failure", err)
	}
}

func TestBackupRejectsUnsafeOutputAndPendingDelete(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	insideMedia := filepath.Join(fixture.mediaRoot, "backup.zip")
	if err := run([]string{"backup", "-db", fixture.dbPath, "-media", fixture.mediaRoot, "-out", insideMedia}); err == nil || !strings.Contains(err.Error(), "media root") {
		t.Fatalf("unsafe output error=%v", err)
	}
	if err := os.MkdirAll(filepath.Join(fixture.mediaRoot, ".deleting", "post-1"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"backup", "-db", fixture.dbPath, "-media", fixture.mediaRoot, "-out", filepath.Join(t.TempDir(), "pending.zip")}); err == nil || !strings.Contains(err.Error(), "ReconcileStagedDeletes") {
		t.Fatalf("pending delete error=%v", err)
	}
}

func TestBackupRejectsMediaReachedThroughSymlinkOutsideRoot(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "original.png"), []byte("outside-original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "thumb.jpg"), []byte("outside-thumb"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fixture.mediaRoot, "linked")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", fixture.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,source) VALUES('draft','linked/original.png','linked/thumb.jpg','image/png',1,1,1,'linked-outside','')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	err = run([]string{"backup", "-db", fixture.dbPath, "-media", fixture.mediaRoot, "-out", filepath.Join(t.TempDir(), "outside-media.zip")})
	if err == nil || !strings.Contains(err.Error(), "outside the media root") {
		t.Fatalf("backup error=%v, want escaped media failure", err)
	}
}

func TestRestoreRejectsNonEmptyDestinations(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if err := run([]string{"backup", "-db", fixture.dbPath, "-media", fixture.mediaRoot, "-out", archivePath}); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	dbPath := filepath.Join(dest, "kura.db")
	mediaRoot := filepath.Join(dest, "media")
	if err := os.WriteFile(dbPath, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mediaRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaRoot, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"restore", "-in", archivePath, "-db", dbPath, "-media", mediaRoot}); err == nil || !strings.Contains(err.Error(), "must not already exist") {
		t.Fatalf("restore error=%v", err)
	}
	if got, err := os.ReadFile(dbPath); err != nil || string(got) != "existing" {
		t.Fatalf("existing database changed: %q, %v", got, err)
	}
}

func TestRestoreRejectsNonEmptyMediaDestination(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if err := run([]string{"backup", "-db", fixture.dbPath, "-media", fixture.mediaRoot, "-out", archivePath}); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	dbPath := filepath.Join(dest, "kura.db")
	mediaRoot := filepath.Join(dest, "media")
	if err := os.MkdirAll(mediaRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaRoot, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"restore", "-in", archivePath, "-db", dbPath, "-media", mediaRoot}); err == nil || !strings.Contains(err.Error(), "must not already exist") {
		t.Fatalf("restore error=%v, want non-empty-media failure", err)
	}
	if _, err := os.Stat(filepath.Join(mediaRoot, "keep")); err != nil {
		t.Fatalf("existing media changed: %v", err)
	}
}

func TestVerifyRejectsMissingReferencedMedia(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if err := run([]string{"backup", "-db", fixture.dbPath, "-media", fixture.mediaRoot, "-out", archivePath}); err != nil {
		t.Fatal(err)
	}
	removeZipEntry(t, archivePath, "media/originals/published.png")
	if err := run([]string{"verify", "-in", archivePath}); err == nil || !strings.Contains(err.Error(), "missing archive entry") {
		t.Fatalf("verify error=%v, want missing media failure", err)
	}
}

func newMaintenanceFixture(t *testing.T) maintenanceFixture {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "source.db")
	mediaRoot := filepath.Join(root, "media")
	if err := os.MkdirAll(mediaRoot, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := archive.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	owner, err := store.Register(ctx, "fixture-owner", "fixture owner password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Register(ctx, "fixture-other", "fixture other password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.NewSession(ctx, &owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.NewSession(ctx, &other.ID); err != nil {
		t.Fatal(err)
	}
	media := map[string][]byte{
		"originals/published.png":   []byte("published-original"),
		"thumbs/published.jpg":      []byte("published-thumb"),
		"originals/draft.png":       []byte("draft-original"),
		"thumbs/draft.jpg":          []byte("draft-thumb"),
		"originals/quarantined.png": []byte("quarantined-original"),
		"thumbs/quarantined.jpg":    []byte("quarantined-thumb"),
		"originals/deleted.png":     []byte("deleted-original"),
		"thumbs/deleted.jpg":        []byte("deleted-thumb"),
	}
	for rel, body := range media {
		path := filepath.Join(mediaRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	posts := []struct {
		status, original, thumb, hash  string
		uploader                       int64
		deleted, quarantined, previous any
	}{
		{"published", "originals/published.png", "thumbs/published.jpg", "hash-published", owner.ID, nil, nil, nil},
		{"draft", "originals/draft.png", "thumbs/draft.jpg", "hash-draft", owner.ID, nil, nil, nil},
		{"published", "originals/quarantined.png", "thumbs/quarantined.jpg", "hash-quarantined", other.ID, nil, "2026-09-17T00:00:00Z", "published"},
		{"published", "originals/deleted.png", "thumbs/deleted.jpg", "hash-deleted", owner.ID, "2026-09-17T00:00:01Z", nil, nil},
	}
	for _, post := range posts {
		if _, err = store.DB.Exec(`INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,source,published_at,uploader_id,deleted_at,quarantined_at,quarantine_reason,quarantine_previous_status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, post.status, post.original, post.thumb, "image/png", 10, 20, len(media[post.original]), post.hash, "fixture", "2026-09-17T00:00:00Z", post.uploader, post.deleted, post.quarantined, func() any {
			if post.quarantined != nil {
				return "fixture review"
			}
			return ""
		}(), post.previous); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.DB.Exec(`INSERT INTO tags(name,display_name,category) VALUES('fixture','Fixture','general'); INSERT INTO pools(slug,name,description,owner_id,status) VALUES('fixture-pool','Fixture pool','round trip',?, 'draft');`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB.Exec(`INSERT INTO favorites(post_id,user_id) SELECT id,? FROM posts WHERE sha256='hash-published'`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB.Exec(`INSERT INTO audit_events(event_type,actor_id,post_id,reason) SELECT 'quarantine',?,?, 'fixture review' FROM posts WHERE sha256='hash-quarantined'`, other.ID, 3); err != nil {
		t.Fatal(err)
	}
	return maintenanceFixture{dbPath: dbPath, mediaRoot: mediaRoot, media: media, counts: map[string]int{
		"SELECT count(*) FROM users":                                  2,
		"SELECT count(*) FROM sessions":                               2,
		"SELECT count(*) FROM posts":                                  4,
		"SELECT count(*) FROM posts WHERE status='draft'":             1,
		"SELECT count(*) FROM posts WHERE deleted_at IS NOT NULL":     1,
		"SELECT count(*) FROM posts WHERE quarantined_at IS NOT NULL": 1,
		"SELECT count(*) FROM pools":                                  1,
		"SELECT count(*) FROM favorites":                              1,
		"SELECT count(*) FROM audit_events":                           1,
	}}
}

func rewriteZip(t *testing.T, name string, mutate func(string, []byte) []byte) {
	t.Helper()
	old, err := zip.OpenReader(name)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	tmp := name + ".rewrite"
	out, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	for _, entry := range old.File {
		body, err := readZipFile(entry)
		if err != nil {
			t.Fatal(err)
		}
		w, err := zw.Create(entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(mutate(entry.Name, body)); err != nil {
			t.Fatal(err)
		}
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = out.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(tmp, name); err != nil {
		t.Fatal(err)
	}
}

func addZipEntry(t *testing.T, name, entryName string, body []byte) {
	t.Helper()
	rewriteZip(t, name, func(existing string, existingBody []byte) []byte { return existingBody })
	old, err := zip.OpenReader(name)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	tmp := name + ".add"
	out, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	for _, entry := range old.File {
		w, _ := zw.Create(entry.Name)
		content, _ := readZipFile(entry)
		_, _ = w.Write(content)
	}
	w, err := zw.Create(entryName)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(body)
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = out.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(tmp, name); err != nil {
		t.Fatal(err)
	}
}

func removeZipEntry(t *testing.T, name, remove string) {
	t.Helper()
	old, err := zip.OpenReader(name)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	tmp := name + ".remove"
	out, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	for _, entry := range old.File {
		if entry.Name == remove {
			continue
		}
		w, _ := zw.Create(entry.Name)
		content, _ := readZipFile(entry)
		_, _ = w.Write(content)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = out.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(tmp, name); err != nil {
		t.Fatal(err)
	}
}

func readZipFile(entry *zip.File) ([]byte, error) {
	reader, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}
