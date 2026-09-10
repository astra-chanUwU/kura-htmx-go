package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	demomedia "kura/demo"
	"kura/internal/archive"
	"kura/internal/web"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dbPath := flag.String("db", "var/kura.db", "SQLite database path")
	media := flag.String("media", "var/media", "media root")
	seed := flag.Bool("seed", false, "add demo posts if empty")
	bootstrap := flag.Bool("bootstrap-super-admin", false, "print a short-lived setup URL for the initial super admin")
	flag.Parse()
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0755); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(*media, 0755); err != nil {
		log.Fatal(err)
	}
	store, err := archive.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	if *bootstrap {
		token, err := store.CreateBootstrapToken(context.Background(), 15*time.Minute)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("Open this one-use Kura setup URL within 15 minutes: %s", setupURL(*addr, os.Getenv("KURA_ORIGINS"), token))
	}
	if *seed {
		if err := seedDemo(context.Background(), store, *media); err != nil {
			log.Fatal(err)
		}
	}
	server, err := web.New(store, *media)
	if err != nil {
		log.Fatal(err)
	}
	displayAddr := *addr
	if strings.HasPrefix(displayAddr, ":") {
		displayAddr = "localhost" + displayAddr
	}
	log.Printf("Kura listening on http://%s", displayAddr)
	log.Fatal(http.ListenAndServe(*addr, server.Handler()))
}

func setupURL(addr, configuredOrigins, token string) string {
	origin := ""
	if configuredOrigins != "" {
		origin = strings.TrimSpace(strings.Split(configuredOrigins, ",")[0])
	}
	if origin == "" {
		displayAddr := addr
		if strings.HasPrefix(displayAddr, ":") {
			displayAddr = "localhost" + displayAddr
		}
		origin = "http://" + displayAddr
	}
	return strings.TrimRight(origin, "/") + "/setup?token=" + url.QueryEscape(token)
}

func seedDemo(ctx context.Context, s *archive.Store, root string) error {
	entries, err := demomedia.Files.ReadDir("images")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	if err := os.MkdirAll(filepath.Join(root, "originals", "demo"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, "thumbs", "demo"), 0755); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO tags(name,display_name,category) VALUES('sample','Sample','general'),('demo_import','Demo import','meta') ON CONFLICT(name) DO NOTHING`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO pools(slug,name,description) VALUES('sample-set','Sample set','Local images supplied for the Kura demo.') ON CONFLICT(slug) DO NOTHING`); err != nil {
		return err
	}
	for i, entry := range entries {
		name := entry.Name()
		rel := filepath.Join("originals", "demo", name)
		thumbName := strings.TrimSuffix(name, filepath.Ext(name)) + ".jpg"
		thumb := filepath.Join("thumbs", "demo", thumbName)
		body, err := demomedia.Files.ReadFile("images/" + name)
		if err != nil {
			return err
		}
		thumbBody, err := demomedia.Files.ReadFile("thumbs/" + thumbName)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(root, rel), body, 0644); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(root, thumb), thumbBody, 0644); err != nil {
			return err
		}
		config, _, err := image.DecodeConfig(bytes.NewReader(body))
		if err != nil {
			return err
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(body))
		_, err = tx.ExecContext(ctx, `INSERT INTO posts(status,original_path,thumbnail_path,mime_type,width,height,byte_size,sha256,source,published_at) VALUES('published',?,?,?,?,?,?,?,?,?) ON CONFLICT(sha256) DO NOTHING`, rel, thumb, http.DetectContentType(body), config.Width, config.Height, len(body), sum, "", time.Now().Add(-time.Duration(i)*time.Minute).UTC().Format(time.RFC3339))
		if err != nil {
			return err
		}
		var pid int64
		if err = tx.QueryRowContext(ctx, `SELECT id FROM posts WHERE sha256=?`, sum).Scan(&pid); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO post_tags(post_id,tag_id) SELECT ?,id FROM tags WHERE name IN ('sample','demo_import') ON CONFLICT DO NOTHING`, pid); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO pool_posts(pool_id,post_id,position) SELECT id,?,? FROM pools WHERE slug='sample-set' ON CONFLICT DO NOTHING`, pid, i+1); err != nil {
			return err
		}
	}
	return tx.Commit()
}
