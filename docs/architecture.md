# Architecture and data model

## Shape

Kura is a modular monolith. `net/http` serves HTML rendered from embedded Go templates. HTMX requests return the same server-owned browsing model as a partial. SQLite owns metadata and relationships. Originals and thumbnails live below one configured media root and are served by the same process.

There is no background service in v1. Upload work can extract metadata and create a thumbnail inside the request, using a temporary file followed by an atomic rename. If measurements later show that processing blocks normal use, a database-backed job table is the smallest justified next step.

## Initial data model

- `posts`: lifecycle (`draft` or `published`), original and thumbnail paths, MIME, dimensions, byte size, SHA-256, source, timestamps.
- `tags`: unique normalized name, display name, and fixed category (`artist`, `character`, `copyright`, `general`, `meta`).
- `post_tags`: many-to-many post/tag assignment with assignment time.
- `pools`: named collection with stable slug and description.
- `pool_posts`: ordered many-to-many pool membership.
- `favorites`: post and local owner key, retained as a relationship so future authentication does not change post records.
- `schema_migrations`: applied migration versions.

SQLite foreign keys are enabled. Hashes are unique. Tag names and pool slugs are unique. Published-time and join indexes support the first browsing paths.

## Filesystem contract

The database stores paths relative to the media root, never arbitrary absolute paths. The intended layout is `originals/YYYY/MM/<hash>.<ext>` and `thumbs/YYYY/MM/<hash>.jpg`. Demo data uses `demo/` beneath those roots. Database deletion must not silently delete media; cleanup should be an explicit maintenance operation.
