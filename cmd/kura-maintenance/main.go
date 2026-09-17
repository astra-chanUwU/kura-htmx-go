package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kura/internal/archive"

	"github.com/mattn/go-sqlite3"
)

const (
	manifestName    = "manifest.json"
	databaseName    = "database/kura.sqlite"
	mediaPrefix     = "media/"
	manifestVersion = 1
)

type manifest struct {
	Version   int              `json:"version"`
	CreatedAt string           `json:"created_at"`
	Entries   []manifestEntry  `json:"entries"`
	Counts    map[string]int64 `json:"counts"`
}

type manifestEntry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type verifiedArchive struct {
	Dir      string
	Manifest manifest
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "kura-maintenance:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use one of: backup, check, verify, restore")
	}
	switch args[0] {
	case "backup":
		return runBackup(args[1:])
	case "check":
		return runCheck(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "restore":
		return runRestore(args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q; use backup, check, verify, or restore", args[0])
	}
}

func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dbPath := fs.String("db", "", "SQLite database path")
	mediaRoot := fs.String("media", "", "configured media root")
	outPath := fs.String("out", "", "output archive path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlags("backup", map[string]string{"-db": *dbPath, "-media": *mediaRoot, "-out": *outPath}); err != nil {
		return err
	}
	return backup(*dbPath, *mediaRoot, *outPath)
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	inPath := fs.String("in", "", "archive path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlags("verify", map[string]string{"-in": *inPath}); err != nil {
		return err
	}
	checked, err := inspectArchive(*inPath)
	if checked.Dir != "" {
		defer os.RemoveAll(checked.Dir)
	}
	return err
}

func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	inPath := fs.String("in", "", "archive path")
	dbPath := fs.String("db", "", "new SQLite database destination")
	mediaRoot := fs.String("media", "", "new media root destination")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlags("restore", map[string]string{"-in": *inPath, "-db": *dbPath, "-media": *mediaRoot}); err != nil {
		return err
	}
	return restore(*inPath, *dbPath, *mediaRoot)
}

func requireFlags(command string, values map[string]string) error {
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s requires %s", command, name)
		}
	}
	return nil
}

func backup(dbPath, mediaRoot, outPath string) error {
	dbPath, mediaRoot, outPath, err := validateBackupPaths(dbPath, mediaRoot, outPath)
	if err != nil {
		return err
	}
	if err = rejectPendingStaging(mediaRoot); err != nil {
		return err
	}
	source, err := openReadOnly(dbPath)
	if err != nil {
		return fmt.Errorf("open source database read-only: %w", err)
	}
	defer source.Close()
	if err = validateDatabaseConnection(source, true); err != nil {
		return fmt.Errorf("validate source database: %w", err)
	}
	refs, counts, err := collectMediaReferences(source)
	if err != nil {
		return fmt.Errorf("read source media metadata: %w", err)
	}

	stage, err := os.MkdirTemp(filepath.Dir(outPath), ".kura-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	databasePath := filepath.Join(stage, "kura.sqlite")
	if err = snapshotSQLite(dbPath, databasePath); err != nil {
		return fmt.Errorf("create SQLite snapshot: %w", err)
	}
	if err = validateDatabaseFile(databasePath); err != nil {
		return fmt.Errorf("validate SQLite snapshot: %w", err)
	}

	entries := make([]manifestEntry, 0, len(refs)+1)
	databaseEntry, err := fileEntry(databaseName, "database", databasePath)
	if err != nil {
		return err
	}
	entries = append(entries, databaseEntry)
	for _, rel := range refs {
		entryPath := mediaPrefix + rel
		sourcePath, pathErr := confinedMediaFile(mediaRoot, rel)
		if pathErr != nil {
			return fmt.Errorf("media %s: %w", rel, pathErr)
		}
		entry, entryErr := fileEntry(entryPath, "media", sourcePath)
		if entryErr != nil {
			return fmt.Errorf("media %s: %w", rel, entryErr)
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	counts["media_files"] = int64(len(refs))
	counts["media_bytes"] = 0
	for _, entry := range entries {
		if entry.Kind == "media" {
			counts["media_bytes"] += entry.Size
		}
	}
	manifestBytes, err := json.MarshalIndent(manifest{Version: manifestVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339), Entries: entries, Counts: counts}, "", "  ")
	if err != nil {
		return err
	}

	tmpArchive := filepath.Join(stage, "backup.zip")
	file, err := os.OpenFile(tmpArchive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	zipWriter := zip.NewWriter(file)
	for _, entry := range entries {
		sourcePath := databasePath
		if entry.Kind == "media" {
			sourcePath, err = confinedMediaFile(mediaRoot, strings.TrimPrefix(entry.Path, mediaPrefix))
			if err != nil {
				_ = zipWriter.Close()
				_ = file.Close()
				return err
			}
		}
		if err = addFileToZip(zipWriter, entry.Path, sourcePath); err != nil {
			_ = zipWriter.Close()
			_ = file.Close()
			return err
		}
	}
	manifestFile, err := zipWriter.Create(manifestName)
	if err == nil {
		_, err = manifestFile.Write(manifestBytes)
	}
	if closeErr := zipWriter.Close(); err == nil {
		err = closeErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	if err = os.Rename(tmpArchive, outPath); err != nil {
		return fmt.Errorf("publish archive: %w", err)
	}
	return nil
}

func restore(inPath, dbPath, mediaRoot string) error {
	inPath, err := absoluteRegularFile(inPath, "archive")
	if err != nil {
		return err
	}
	dbPath, mediaRoot, err = validateRestoreDestinations(dbPath, mediaRoot)
	if err != nil {
		return err
	}
	checked, err := inspectArchive(inPath)
	if checked.Dir != "" {
		defer os.RemoveAll(checked.Dir)
	}
	if err != nil {
		return err
	}
	stageDBFile, err := os.CreateTemp(filepath.Dir(dbPath), ".kura-restore-db-")
	if err != nil {
		return err
	}
	stageDB := stageDBFile.Name()
	if err = stageDBFile.Close(); err != nil {
		_ = os.Remove(stageDB)
		return err
	}
	defer os.Remove(stageDB)
	if err = copyFile(filepath.Join(checked.Dir, "database.sqlite"), stageDB); err != nil {
		return fmt.Errorf("stage database: %w", err)
	}
	stageMedia, err := os.MkdirTemp(filepath.Dir(mediaRoot), ".kura-restore-media-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageMedia)
	if err = copyDirectory(filepath.Join(checked.Dir, "media"), stageMedia); err != nil {
		return fmt.Errorf("stage media: %w", err)
	}
	if err = os.Rename(stageMedia, mediaRoot); err != nil {
		return fmt.Errorf("install restored media: %w", err)
	}
	mediaInstalled := true
	if err = os.Rename(stageDB, dbPath); err != nil {
		if mediaInstalled {
			_ = os.Rename(mediaRoot, stageMedia)
		}
		return fmt.Errorf("install restored database: %w", err)
	}
	return nil
}

func validateBackupPaths(dbPath, mediaRoot, outPath string) (string, string, string, error) {
	dbPath, err := absoluteRegularFile(dbPath, "source database")
	if err != nil {
		return "", "", "", err
	}
	mediaRoot, err = absoluteDirectory(mediaRoot, "media root")
	if err != nil {
		return "", "", "", err
	}
	outPath, err = absolutePath(outPath)
	if err != nil {
		return "", "", "", err
	}
	if _, err = os.Lstat(outPath); err == nil {
		return "", "", "", fmt.Errorf("output archive %q already exists; choose a new path", outPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", "", fmt.Errorf("inspect output archive: %w", err)
	}
	if _, err = existingDirectory(filepath.Dir(outPath), "output parent"); err != nil {
		return "", "", "", err
	}
	mediaReal, err := filepath.EvalSymlinks(mediaRoot)
	if err != nil {
		return "", "", "", err
	}
	dbReal, err := filepath.EvalSymlinks(dbPath)
	if err != nil {
		return "", "", "", err
	}
	outParentReal, err := filepath.EvalSymlinks(filepath.Dir(outPath))
	if err != nil {
		return "", "", "", err
	}
	outReal := filepath.Join(outParentReal, filepath.Base(outPath))
	if within(mediaReal, outReal) || within(mediaReal, dbReal) {
		return "", "", "", errors.New("database and archive outputs must not be inside the media root")
	}
	if samePath(dbReal, outReal) {
		return "", "", "", errors.New("output archive must not replace the source database")
	}
	return dbPath, mediaRoot, outPath, nil
}

func validateRestoreDestinations(dbPath, mediaRoot string) (string, string, error) {
	dbPath, err := absolutePath(dbPath)
	if err != nil {
		return "", "", err
	}
	mediaRoot, err = absolutePath(mediaRoot)
	if err != nil {
		return "", "", err
	}
	if samePath(dbPath, mediaRoot) || within(mediaRoot, dbPath) || within(dbPath, mediaRoot) {
		return "", "", errors.New("database and media destinations must be separate paths")
	}
	for _, item := range []struct{ path, label string }{{dbPath, "database destination"}, {mediaRoot, "media destination"}} {
		if _, statErr := os.Lstat(item.path); statErr == nil {
			return "", "", fmt.Errorf("%s %q must not already exist; choose new destinations", item.label, item.path)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", "", fmt.Errorf("inspect %s: %w", item.label, statErr)
		}
		if _, err = existingDirectory(filepath.Dir(item.path), item.label+" parent"); err != nil {
			return "", "", err
		}
	}
	return dbPath, mediaRoot, nil
}

func inspectArchive(inPath string) (verifiedArchive, error) {
	result := verifiedArchive{}
	reader, err := zip.OpenReader(inPath)
	if err != nil {
		return result, fmt.Errorf("open archive: %w", err)
	}
	defer reader.Close()
	seen := make(map[string]bool)
	var manifestEntryFile *zip.File
	archiveFiles := make(map[string]*zip.File)
	for _, entry := range reader.File {
		if seen[entry.Name] {
			return result, fmt.Errorf("duplicate archive entry %q", entry.Name)
		}
		seen[entry.Name] = true
		if entry.FileInfo().IsDir() || strings.HasSuffix(entry.Name, "/") {
			return result, fmt.Errorf("archive directories are not allowed: %q", entry.Name)
		}
		switch {
		case entry.Name == manifestName:
			manifestEntryFile = entry
		case entry.Name == databaseName:
			archiveFiles[entry.Name] = entry
		case strings.HasPrefix(entry.Name, mediaPrefix):
			if _, err = safeArchiveMediaPath(strings.TrimPrefix(entry.Name, mediaPrefix)); err != nil {
				return result, err
			}
			archiveFiles[entry.Name] = entry
		default:
			return result, fmt.Errorf("unexpected archive entry %q", entry.Name)
		}
	}
	if manifestEntryFile == nil {
		return result, errors.New("archive is missing manifest.json")
	}
	manifestBytes, err := readZipEntry(manifestEntryFile, 16<<20)
	if err != nil {
		return result, fmt.Errorf("read manifest: %w", err)
	}
	if err = json.Unmarshal(manifestBytes, &result.Manifest); err != nil {
		return result, fmt.Errorf("decode manifest: %w", err)
	}
	if result.Manifest.Version != manifestVersion {
		return result, fmt.Errorf("unsupported manifest version %d", result.Manifest.Version)
	}
	if result.Manifest.CreatedAt == "" {
		return result, errors.New("manifest creation time is missing")
	}
	manifestEntries := make(map[string]manifestEntry, len(result.Manifest.Entries))
	for _, item := range result.Manifest.Entries {
		if item.Path == manifestName || seen[item.Path] == false {
			return result, fmt.Errorf("manifest references missing archive entry %q", item.Path)
		}
		if _, duplicate := manifestEntries[item.Path]; duplicate {
			return result, fmt.Errorf("duplicate manifest entry %q", item.Path)
		}
		if item.Size < 0 || len(item.SHA256) != sha256.Size*2 {
			return result, fmt.Errorf("invalid manifest metadata for %q", item.Path)
		}
		if _, err = hex.DecodeString(item.SHA256); err != nil {
			return result, fmt.Errorf("invalid SHA-256 for %q", item.Path)
		}
		if item.Kind != "database" && item.Kind != "media" {
			return result, fmt.Errorf("invalid entry kind for %q", item.Path)
		}
		if item.Kind == "database" && item.Path != databaseName {
			return result, fmt.Errorf("database entry has invalid path %q", item.Path)
		}
		if item.Kind == "media" {
			if !strings.HasPrefix(item.Path, mediaPrefix) {
				return result, fmt.Errorf("media entry has invalid path %q", item.Path)
			}
			if _, err = safeArchiveMediaPath(strings.TrimPrefix(item.Path, mediaPrefix)); err != nil {
				return result, err
			}
		}
		manifestEntries[item.Path] = item
	}
	if _, ok := manifestEntries[databaseName]; !ok {
		return result, errors.New("manifest is missing the database entry")
	}
	if len(manifestEntries) != len(archiveFiles) {
		return result, errors.New("manifest and archive entries do not match")
	}
	for name, item := range manifestEntries {
		if archiveFiles[name].UncompressedSize64 != uint64(item.Size) {
			return result, fmt.Errorf("size mismatch for %q", name)
		}
	}
	result.Dir, err = os.MkdirTemp("", "kura-maintenance-verify-")
	if err != nil {
		return result, err
	}
	cleanupOnError := true
	defer func() {
		if cleanupOnError {
			_ = os.RemoveAll(result.Dir)
		}
	}()
	if err = os.Mkdir(filepath.Join(result.Dir, "media"), 0700); err != nil {
		return verifiedArchive{}, err
	}
	for name, item := range manifestEntries {
		destination := filepath.Join(result.Dir, "database.sqlite")
		if item.Kind == "media" {
			rel := strings.TrimPrefix(name, mediaPrefix)
			destination = filepath.Join(result.Dir, "media", filepath.FromSlash(rel))
			if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
				return verifiedArchive{}, err
			}
		}
		if err = extractAndCheck(archiveFiles[name], destination, item); err != nil {
			return verifiedArchive{}, err
		}
	}
	if err = validateDatabaseFile(filepath.Join(result.Dir, "database.sqlite")); err != nil {
		return verifiedArchive{}, fmt.Errorf("archive database: %w", err)
	}
	if err = validateReferencedMedia(filepath.Join(result.Dir, "database.sqlite"), filepath.Join(result.Dir, "media"), manifestEntries); err != nil {
		return verifiedArchive{}, err
	}
	cleanupOnError = false
	return result, nil
}

func validateReferencedMedia(dbPath, mediaRoot string, entries map[string]manifestEntry) error {
	store, err := archive.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open and migrate archive database: %w", err)
	}
	defer store.Close()
	rows, err := store.DB.Query(`SELECT original_path,thumbnail_path FROM posts`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var original, thumbnail string
		if err = rows.Scan(&original, &thumbnail); err != nil {
			return err
		}
		for _, raw := range []string{original, thumbnail} {
			rel, pathErr := safeMediaPath(raw)
			if pathErr != nil {
				return fmt.Errorf("database media path %q: %w", raw, pathErr)
			}
			if _, ok := entries[mediaPrefix+rel]; !ok {
				return fmt.Errorf("missing archive entry for referenced media %q", rel)
			}
			filePath := filepath.Join(mediaRoot, filepath.FromSlash(rel))
			info, statErr := os.Stat(filePath)
			if statErr != nil {
				return fmt.Errorf("referenced media %q: %w", rel, statErr)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("referenced media %q is not a regular file", rel)
			}
		}
	}
	return rows.Err()
}

func validateDatabaseFile(dbPath string) error {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = validateDatabaseConnection(db, true); err != nil {
		return err
	}
	store, err := archive.Open(dbPath)
	if err != nil {
		return fmt.Errorf("run Kura migrations: %w", err)
	}
	return store.Close()
}

func validateDatabaseConnection(db *sql.DB, requireCurrent bool) error {
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check returned %q", result)
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	if rows.Next() {
		rows.Close()
		return errors.New("foreign_key_check found a violation")
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if !requireCurrent {
		return nil
	}
	versions := make(map[string]bool)
	rows, err = db.Query("SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("read schema migrations: %w", err)
	}
	for rows.Next() {
		var version string
		if err = rows.Scan(&version); err != nil {
			rows.Close()
			return err
		}
		versions[version] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, version := range archive.CurrentMigrationVersions() {
		if !versions[version] {
			return fmt.Errorf("schema migration %s is not applied", strings.TrimSuffix(version, ".sql"))
		}
	}
	return nil
}

func collectMediaReferences(db *sql.DB) ([]string, map[string]int64, error) {
	rows, err := db.Query(`SELECT original_path,thumbnail_path FROM posts`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	refs := map[string]bool{}
	for rows.Next() {
		var original, thumbnail string
		if err = rows.Scan(&original, &thumbnail); err != nil {
			return nil, nil, err
		}
		for _, raw := range []string{original, thumbnail} {
			rel, pathErr := safeMediaPath(raw)
			if pathErr != nil {
				return nil, nil, fmt.Errorf("database media path %q: %w", raw, pathErr)
			}
			refs[rel] = true
		}
	}
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	ordered := make([]string, 0, len(refs))
	for rel := range refs {
		ordered = append(ordered, rel)
	}
	sort.Strings(ordered)
	counts := make(map[string]int64)
	queries := map[string]string{
		"users": "SELECT count(*) FROM users", "sessions": "SELECT count(*) FROM sessions", "posts": "SELECT count(*) FROM posts",
		"posts_draft": "SELECT count(*) FROM posts WHERE status='draft'", "posts_published": "SELECT count(*) FROM posts WHERE status='published'",
		"posts_deleted": "SELECT count(*) FROM posts WHERE deleted_at IS NOT NULL", "posts_quarantined": "SELECT count(*) FROM posts WHERE quarantined_at IS NOT NULL",
		"pools": "SELECT count(*) FROM pools", "favorites": "SELECT count(*) FROM favorites", "tags": "SELECT count(*) FROM tags", "audit_events": "SELECT count(*) FROM audit_events",
	}
	for name, query := range queries {
		var count int64
		if err = db.QueryRow(query).Scan(&count); err != nil {
			return nil, nil, fmt.Errorf("count %s: %w", name, err)
		}
		counts[name] = count
	}
	return ordered, counts, nil
}

func snapshotSQLite(sourcePath, destinationPath string) error {
	source, err := openReadOnly(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := sql.Open("sqlite3", destinationPath)
	if err != nil {
		return err
	}
	defer destination.Close()
	ctx := context.Background()
	sourceConn, err := source.Conn(ctx)
	if err != nil {
		return err
	}
	defer sourceConn.Close()
	destinationConn, err := destination.Conn(ctx)
	if err != nil {
		return err
	}
	defer destinationConn.Close()
	return destinationConn.Raw(func(destinationRaw any) error {
		destinationSQLite, ok := destinationRaw.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("unexpected SQLite destination driver")
		}
		return sourceConn.Raw(func(sourceRaw any) error {
			sourceSQLite, ok := sourceRaw.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("unexpected SQLite source driver")
			}
			backup, err := destinationSQLite.Backup("main", sourceSQLite, "main")
			if err != nil {
				return err
			}
			defer backup.Close()
			for {
				done, stepErr := backup.Step(-1)
				if stepErr != nil {
					return stepErr
				}
				if done {
					break
				}
			}
			return backup.Finish()
		})
	})
}

func openReadOnly(dbPath string) (*sql.DB, error) {
	dsn := (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&_query_only=true&_foreign_keys=true"}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func fileEntry(name, kind, sourcePath string) (manifestEntry, error) {
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return manifestEntry{}, err
	}
	if !info.Mode().IsRegular() {
		return manifestEntry{}, errors.New("media must be a regular file, not a directory or symlink")
	}
	hash, err := hashFile(sourcePath)
	if err != nil {
		return manifestEntry{}, err
	}
	return manifestEntry{Path: name, Kind: kind, Size: info.Size(), SHA256: hash}, nil
}

func addFileToZip(writer *zip.Writer, name, sourcePath string) error {
	input, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	defer input.Close()
	output, err := writer.Create(name)
	if err != nil {
		return err
	}
	if _, err = io.Copy(output, input); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func extractAndCheck(entry *zip.File, destination string, want manifestEntry) error {
	input, err := entry.Open()
	if err != nil {
		return fmt.Errorf("open archive entry %q: %w", entry.Name, err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("stage archive entry %q: %w", entry.Name, err)
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hasher), input)
	closeErr := output.Close()
	if copyErr != nil {
		return fmt.Errorf("extract archive entry %q: %w", entry.Name, copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if written != want.Size {
		return fmt.Errorf("size mismatch for %q", entry.Name)
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != want.SHA256 {
		return fmt.Errorf("checksum mismatch for %q", entry.Name)
	}
	return nil
}

func readZipEntry(entry *zip.File, max int64) ([]byte, error) {
	if entry.UncompressedSize64 > uint64(max) {
		return nil, errors.New("manifest is too large")
	}
	input, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer input.Close()
	return io.ReadAll(io.LimitReader(input, max+1))
}

func hashFile(name string) (string, error) {
	input, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer input.Close()
	hasher := sha256.New()
	if _, err = io.Copy(hasher, input); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source %q: %w", source, err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("open destination %q: %w", destination, err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func copyDirectory(source, destination string) error {
	return filepath.WalkDir(source, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.Mkdir(target, 0700)
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("staged media entry %q is not a regular file", rel)
		}
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		return copyFile(name, target)
	})
}

func safeMediaPath(raw string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, 0) || strings.Contains(raw, "\\") || filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" {
		return "", errors.New("media path must be relative and confined to the media root")
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != raw {
		return "", errors.New("media path must be relative and confined to the media root")
	}
	return clean, nil
}

func safeArchiveMediaPath(raw string) (string, error) {
	if _, err := safeMediaPath(raw); err != nil {
		return "", fmt.Errorf("unsafe archive path %q: %w", raw, err)
	}
	return raw, nil
}

func confinedMediaFile(mediaRoot, rel string) (string, error) {
	root, err := filepath.EvalSymlinks(mediaRoot)
	if err != nil {
		return "", err
	}
	candidate, err := filepath.EvalSymlinks(filepath.Join(mediaRoot, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	if !within(root, candidate) {
		return "", errors.New("media path resolves outside the media root")
	}
	return candidate, nil
}

func rejectPendingStaging(mediaRoot string) error {
	for _, name := range []string{".deleting", ".incoming"} {
		stage := filepath.Join(mediaRoot, name)
		entries, err := os.ReadDir(stage)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect media staging %s: %w", name, err)
		}
		if len(entries) > 0 {
			if name == ".deleting" {
				return errors.New("media has pending .deleting staging; stop Kura and run Ingestor.ReconcileStagedDeletes before backing up")
			}
			return errors.New("media has pending .incoming uploads; stop Kura and resolve them before backing up")
		}
	}
	return nil
}

func absoluteRegularFile(raw, label string) (string, error) {
	name, err := absolutePath(raw)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(name)
	if err != nil {
		return "", fmt.Errorf("%s %q: %w", label, name, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s %q is not a regular file", label, name)
	}
	return name, nil
}

func absoluteDirectory(raw, label string) (string, error) {
	name, err := absolutePath(raw)
	if err != nil {
		return "", err
	}
	return existingDirectory(name, label)
}

func existingDirectory(name, label string) (string, error) {
	info, err := os.Stat(name)
	if err != nil {
		return "", fmt.Errorf("%s %q: %w", label, name, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s %q is not a directory", label, name)
	}
	return name, nil
}

func absolutePath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("path must not be empty")
	}
	return filepath.Abs(filepath.Clean(raw))
}

func within(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	return errA == nil && errB == nil && aa == bb
}
