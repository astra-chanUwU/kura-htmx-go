CREATE TABLE posts (
  id INTEGER PRIMARY KEY, status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','published')),
  original_path TEXT NOT NULL, thumbnail_path TEXT NOT NULL, mime_type TEXT NOT NULL,
  width INTEGER NOT NULL CHECK(width>0), height INTEGER NOT NULL CHECK(height>0), byte_size INTEGER NOT NULL CHECK(byte_size>=0),
  sha256 TEXT NOT NULL UNIQUE, source TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, published_at TEXT
);
CREATE INDEX posts_published_at ON posts(status,published_at DESC,id DESC);
CREATE TABLE tags (id INTEGER PRIMARY KEY,name TEXT NOT NULL UNIQUE,display_name TEXT NOT NULL,category TEXT NOT NULL CHECK(category IN ('artist','character','copyright','general','meta')),created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE post_tags (post_id INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,assigned_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,PRIMARY KEY(post_id,tag_id));
CREATE INDEX post_tags_tag_post ON post_tags(tag_id,post_id);
CREATE TABLE pools (id INTEGER PRIMARY KEY,slug TEXT NOT NULL UNIQUE,name TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE pool_posts (pool_id INTEGER NOT NULL REFERENCES pools(id) ON DELETE CASCADE,post_id INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,position INTEGER NOT NULL,PRIMARY KEY(pool_id,post_id),UNIQUE(pool_id,position));
CREATE TABLE favorites (post_id INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,owner_key TEXT NOT NULL DEFAULT 'local',created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,PRIMARY KEY(post_id,owner_key));
