package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kura/internal/archive"
)

type checkReport struct {
	Posts, Referenced, Orphans int
	Findings                   []string
	references                 map[string]bool
}

func runCheck(args []string) error {
	fs := flagSet("check")
	dbPath := fs.String("db", "", "SQLite database path")
	mediaRoot := fs.String("media", "", "configured media root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlags("check", map[string]string{"-db": *dbPath, "-media": *mediaRoot}); err != nil {
		return err
	}
	report, err := checkArchive(*dbPath, *mediaRoot)
	if err != nil {
		return err
	}
	writeCheckReport(os.Stdout, report)
	if len(report.Findings) > 0 {
		return fmt.Errorf("archive health check found %d finding(s)", len(report.Findings))
	}
	return nil
}

func flagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func writeCheckReport(w io.Writer, report checkReport) {
	if len(report.Findings) == 0 {
		fmt.Fprintln(w, "CHECK PASS")
	} else {
		fmt.Fprintln(w, "CHECK FAIL")
		for _, finding := range report.Findings {
			fmt.Fprintf(w, "finding: %s\n", finding)
		}
	}
	fmt.Fprintf(w, "posts: %d\nreferenced media: %d\norphan media: %d\n", report.Posts, report.Referenced, report.Orphans)
}

func checkArchive(dbPath, mediaRoot string) (checkReport, error) {
	var report checkReport
	dbPath, err := absoluteRegularFile(dbPath, "database")
	if err != nil {
		return report, err
	}
	mediaRoot, err = absoluteDirectory(mediaRoot, "media root")
	if err != nil {
		return report, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(mediaRoot)
	if err != nil {
		return report, fmt.Errorf("resolve media root: %w", err)
	}

	db, err := openReadOnly(dbPath)
	if err != nil {
		return report, fmt.Errorf("open database read-only: %w", err)
	}
	defer db.Close()
	checkDatabase(db, &report)
	checkPosts(db, resolvedRoot, mediaRoot, &report)
	checkManagedMedia(resolvedRoot, mediaRoot, &report)
	checkStaging(resolvedRoot, mediaRoot, &report)
	sort.Strings(report.Findings)
	return report, nil
}

func checkDatabase(db *sql.DB, report *checkReport) {
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		report.Findings = append(report.Findings, fmt.Sprintf("database integrity check: %v", err))
	} else if result != "ok" {
		report.Findings = append(report.Findings, fmt.Sprintf("database integrity check: %s", result))
	}

	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		report.Findings = append(report.Findings, fmt.Sprintf("database foreign-key check: %v", err))
	} else {
		for rows.Next() {
			var table string
			var rowID, parent string
			var foreignKey int
			if scanErr := rows.Scan(&table, &rowID, &parent, &foreignKey); scanErr != nil {
				report.Findings = append(report.Findings, fmt.Sprintf("database foreign-key check: %v", scanErr))
				break
			}
			report.Findings = append(report.Findings, fmt.Sprintf("database foreign-key violation: table=%s rowid=%s parent=%s fkid=%d", table, rowID, parent, foreignKey))
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			report.Findings = append(report.Findings, fmt.Sprintf("database foreign-key check: %v", rowsErr))
		}
		_ = rows.Close()
	}

	versions := map[string]bool{}
	rows, err = db.Query("SELECT version FROM schema_migrations")
	if err != nil {
		report.Findings = append(report.Findings, fmt.Sprintf("database schema migrations: %v", err))
	} else {
		for rows.Next() {
			var version string
			if scanErr := rows.Scan(&version); scanErr != nil {
				report.Findings = append(report.Findings, fmt.Sprintf("database schema migrations: %v", scanErr))
				break
			}
			versions[version] = true
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			report.Findings = append(report.Findings, fmt.Sprintf("database schema migrations: %v", rowsErr))
		}
		_ = rows.Close()
	}
	for _, required := range archive.CurrentMigrationVersions() {
		if !versions[required] {
			report.Findings = append(report.Findings, fmt.Sprintf("schema migration %s is not applied", strings.TrimSuffix(required, ".sql")))
		}
	}
}

func checkPosts(db *sql.DB, resolvedRoot, mediaRoot string, report *checkReport) {
	rows, err := db.Query(`SELECT id,original_path,thumbnail_path,byte_size,sha256 FROM posts ORDER BY id`)
	if err != nil {
		report.Findings = append(report.Findings, fmt.Sprintf("read posts: %v", err))
		return
	}
	defer rows.Close()
	references := map[string]bool{}
	for rows.Next() {
		var id int64
		var original, thumbnail, wantHash string
		var wantSize int64
		if err = rows.Scan(&id, &original, &thumbnail, &wantSize, &wantHash); err != nil {
			report.Findings = append(report.Findings, fmt.Sprintf("read post row: %v", err))
			break
		}
		report.Posts++
		for _, item := range []struct {
			label, raw string
		}{{"original", original}, {"thumbnail", thumbnail}} {
			rel, pathErr := safeMediaPath(item.raw)
			if pathErr != nil {
				report.Findings = append(report.Findings, fmt.Sprintf("post %d %s %q: %v", id, item.label, item.raw, pathErr))
				continue
			}
			references[rel] = true
			resolved, fileErr := checkedMediaFile(resolvedRoot, mediaRoot, rel)
			if fileErr != nil {
				report.Findings = append(report.Findings, fmt.Sprintf("post %d %s %q: %v", id, item.label, rel, fileErr))
				continue
			}
			if item.label == "original" {
				info, statErr := os.Stat(resolved)
				if statErr != nil {
					report.Findings = append(report.Findings, fmt.Sprintf("post %d original %q: %v", id, rel, statErr))
					continue
				}
				if info.Size() != wantSize {
					report.Findings = append(report.Findings, fmt.Sprintf("post %d original %q: byte size %d, recorded %d", id, rel, info.Size(), wantSize))
				}
				gotHash, hashErr := hashFile(resolved)
				if hashErr != nil {
					report.Findings = append(report.Findings, fmt.Sprintf("post %d original %q: hash: %v", id, rel, hashErr))
				} else if !strings.EqualFold(gotHash, wantHash) {
					report.Findings = append(report.Findings, fmt.Sprintf("post %d original %q: SHA-256 %s, recorded %s", id, rel, gotHash, wantHash))
				}
			} else if decodeErr := decodeImage(resolved); decodeErr != nil {
				report.Findings = append(report.Findings, fmt.Sprintf("post %d thumbnail %q: cannot decode image: %v", id, rel, decodeErr))
			}
		}
	}
	if err = rows.Err(); err != nil {
		report.Findings = append(report.Findings, fmt.Sprintf("read posts: %v", err))
	}
	report.Referenced = len(references)
	// The scanner below uses the same set, so keep it on the report while the
	// database rows are still the source of truth for every referenced path.
	report.references = references
}

func checkManagedMedia(resolvedRoot, mediaRoot string, report *checkReport) {
	references := report.references
	report.references = nil
	for _, name := range []string{"originals", "thumbs"} {
		managed := filepath.Join(mediaRoot, name)
		entry, err := os.Lstat(managed)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			report.Findings = append(report.Findings, fmt.Sprintf("managed media %q: %v", name, err))
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			resolved, resolveErr := filepath.EvalSymlinks(managed)
			if resolveErr != nil || !within(resolvedRoot, resolved) {
				report.Findings = append(report.Findings, fmt.Sprintf("unsafe media symlink %q resolves outside media root", name))
			} else {
				report.Findings = append(report.Findings, fmt.Sprintf("unsafe media symlink %q", name))
			}
			continue
		}
		if !entry.IsDir() {
			report.Findings = append(report.Findings, fmt.Sprintf("managed media %q is not a directory", name))
			continue
		}
		_ = filepath.WalkDir(managed, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				report.Findings = append(report.Findings, fmt.Sprintf("managed media %q: %v", relativeMedia(mediaRoot, current), walkErr))
				return nil
			}
			if current == managed {
				return nil
			}
			rel := relativeMedia(mediaRoot, current)
			if entry.Type()&os.ModeSymlink != 0 {
				resolved, resolveErr := filepath.EvalSymlinks(current)
				if resolveErr != nil {
					report.Findings = append(report.Findings, fmt.Sprintf("unsafe media symlink %q: %v", rel, resolveErr))
				} else if !within(resolvedRoot, resolved) {
					report.Findings = append(report.Findings, fmt.Sprintf("unsafe media symlink %q resolves outside media root", rel))
				} else {
					report.Findings = append(report.Findings, fmt.Sprintf("unsafe media symlink %q", rel))
				}
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				report.Findings = append(report.Findings, fmt.Sprintf("media %q: %v", rel, infoErr))
				return nil
			}
			if !info.Mode().IsRegular() {
				report.Findings = append(report.Findings, fmt.Sprintf("media %q is not a regular file", rel))
				return nil
			}
			if !references[rel] {
				report.Orphans++
				report.Findings = append(report.Findings, fmt.Sprintf("orphan media %q", rel))
			}
			return nil
		})
	}
}

func checkStaging(resolvedRoot, mediaRoot string, report *checkReport) {
	for _, name := range []string{".incoming", ".deleting"} {
		stage := filepath.Join(mediaRoot, name)
		entry, err := os.Lstat(stage)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			report.Findings = append(report.Findings, fmt.Sprintf("pending %s staging: %v", name, err))
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			resolved, resolveErr := filepath.EvalSymlinks(stage)
			if resolveErr != nil || !within(resolvedRoot, resolved) {
				report.Findings = append(report.Findings, fmt.Sprintf("unsafe staging symlink %q resolves outside media root", name))
			} else {
				report.Findings = append(report.Findings, fmt.Sprintf("pending %s staging entry %q", name, name))
			}
			continue
		}
		if !entry.IsDir() {
			report.Findings = append(report.Findings, fmt.Sprintf("pending %s staging entry %q", name, name))
			continue
		}
		_ = filepath.WalkDir(stage, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				report.Findings = append(report.Findings, fmt.Sprintf("pending %s staging %q: %v", name, relativeMedia(mediaRoot, current), walkErr))
				return nil
			}
			if current == stage {
				return nil
			}
			rel := relativeMedia(mediaRoot, current)
			if entry.Type()&os.ModeSymlink != 0 {
				resolved, resolveErr := filepath.EvalSymlinks(current)
				if resolveErr != nil || !within(resolvedRoot, resolved) {
					report.Findings = append(report.Findings, fmt.Sprintf("unsafe staging symlink %q resolves outside media root", rel))
				}
			}
			report.Findings = append(report.Findings, fmt.Sprintf("pending %s staging entry %q", name, rel))
			if entry.IsDir() && entry.Type()&os.ModeSymlink != 0 {
				return filepath.SkipDir
			}
			return nil
		})
	}
}

func checkedMediaFile(resolvedRoot, mediaRoot, rel string) (string, error) {
	candidate := filepath.Join(mediaRoot, filepath.FromSlash(rel))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("missing media file")
		}
		return "", fmt.Errorf("cannot resolve media file: %w", err)
	}
	if !within(resolvedRoot, resolved) {
		return "", errors.New("media path resolves outside the media root")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("media path is not a regular file")
	}
	return resolved, nil
}

func decodeImage(name string) error {
	input, err := os.Open(name)
	if err != nil {
		return err
	}
	defer input.Close()
	_, _, err = image.Decode(input)
	return err
}

func relativeMedia(root, name string) string {
	rel, err := filepath.Rel(root, name)
	if err != nil {
		return filepath.ToSlash(name)
	}
	return filepath.ToSlash(rel)
}
