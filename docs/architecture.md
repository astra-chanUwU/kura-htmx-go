# Architecture and data model

## Shape

Kura is a modular monolith. `net/http` serves HTML rendered from embedded Go templates. HTMX requests return the same server-owned browsing model as a partial. SQLite owns metadata and relationships. Originals and thumbnails live below one configured media root and are served by the same process.

There is no background service in v1. Upload work can extract metadata and create a thumbnail inside the request, using a temporary file followed by an atomic rename. If measurements later show that processing blocks normal use, a database-backed job table is the smallest justified next step.

## Data model

- `users`: case-preserving local username, nullable Argon2id password hash, random opaque WebAuthn user handle, viewer/moderator/admin role, suspension state, and the unique super-admin marker.
- `sessions`: SHA-256 hashes of random bearer tokens, CSRF tokens, optional account ownership, expiration, and session-bound fresh-passkey verification time.
- `passkey_credentials`: user ownership, name, unique credential ID, and the public credential record required by `go-webauthn` for verification and counter/flag updates.
- `recovery_codes`: one active high-entropy code hash per account. Plaintext is returned only when generated.
- `auth_challenges`: hashed opaque ceremony handles with server-owned WebAuthn session data, purpose, ownership, and short expiry; consuming a challenge deletes it.
- `bootstrap_tokens`: the hash and expiry of the explicitly requested one-use initial enrollment link.
- `posts`: lifecycle (`draft` or `published`), permanent uploader attribution, logical deletion, original upload basename, original and thumbnail paths, MIME, dimensions, byte size, SHA-256, source, timestamps.
- `tags`: unique normalized name, display name, and fixed category (`artist`, `character`, `copyright`, `general`, `meta`).
- `post_tags`: many-to-many post/tag assignment with assignment time.
- `pools`: owner, draft/published visibility, stable slug, name, and description.
- `pool_posts`: ordered many-to-many pool membership.
- `favorites`: private user-to-post relationship.
- `schema_migrations`: applied migration versions.

SQLite foreign keys are enabled. Hashes are unique. Tag names and pool slugs are unique. Published-time and join indexes support the first browsing paths.

## Filesystem contract

The database stores paths relative to the media root, never arbitrary absolute paths. Real uploads use `originals/YYYY/MM/<sha256>.<ext>` and `thumbs/YYYY/MM/<sha256>.jpg`; demo data uses `demo/` beneath those roots. Ingestion streams to a private temporary directory, validates byte size, MIME and decoded dimensions, retains only the submitted basename, creates the thumbnail, and atomically renames both files into place. The media routes and individual download route resolve only paths referenced by a non-deleted post visible to the current requester, and use non-storing cache headers so publication and authorization checks run again for later requests. Downloads generate bounded names at response time, prefix them with the post ID for collision resistance, and preserve the original bytes and MIME-correct extension. Database deletion is logical and never silently deletes media; filesystem reconciliation remains an explicit maintenance operation.

ZIP exports resolve selected post IDs or an authorized pool through archive-owned visibility queries, then stage a complete temporary archive outside the media tree before sending any response headers. They are limited to 100 unique images and 512 MiB of original bytes. Images are stored without recompression; `manifest.json` records schema version, source identity when applicable, ordered membership, bounded filename, post ID, canonical tags/categories, source, SHA-256, original basename, MIME, dimensions, and byte size. The server rechecks relative media paths, regular-file status, recorded size, and SHA-256 while streaming originals into the temporary ZIP. Any missing, changed, unauthorized, deleted, unsupported, or over-limit member discards the entire temporary output. Temporary output is removed on success, failure, and request cancellation; no database transaction spans file reads or response delivery.

Account suspension deletes that account's active sessions but does not delete the account row. Upload ownership uses a restrictive foreign key, so an uploader account cannot be removed and silently orphan its permanent attribution. Legacy/demo posts may have no owner; every real upload records one.

Security-sensitive archive commands receive the authenticated actor and reload its current account row inside the transaction before changing posts, favorites, pools, or account roles. They derive role, suspension, ownership, and super-admin decisions from that row rather than caller-supplied capability fields; private visibility queries apply the same current-account checks in their protected SQL predicates. HTTP handlers may still perform early checks for useful responses. Self-service account mutations are likewise bound to the reloaded actor, while registration, bootstrap, recovery, and WebAuthn challenge completion retain their separate ceremony boundaries.

Tag input accepts space-separated canonical names. A new unprefixed name is created as `general`; new categorized names use `artist:`, `character:`, `copyright:`, `general:`, or `meta:` before normalization. Existing canonical tags retain their global category: an explicit mismatched prefix is rejected atomically rather than recategorizing the tag. The editor-only tag suggestion endpoint rechecks moderator authorization inside the archive query and returns at most eight existing names, including category metadata; it can therefore include tags used only by private drafts without exposing them to viewers or anonymous callers.

Bulk tag changes are bounded to 24 deduplicated post IDs. Browse selection is client-side for the currently rendered page and is cleared when an HTMX refresh replaces that page; pool views do not participate in this first slice. Preview and apply both reload the current actor and selected non-deleted posts inside archive-owned transaction boundaries. Apply inserts and removes relationships as deltas, never replaces a post's tag set, and validates add/remove category conflicts before creating tags. Invalid selections, stale authority, and global category conflicts roll back the entire batch. Preview is non-mutating and reports additions, removals, and no-ops per post; ordinary apply redirects while HTMX apply returns a fragment without changing history.

WebAuthn verification is delegated to the maintained `github.com/go-webauthn/webauthn` library. Kura requires discoverable credentials and user verification, validates configured origins against the RP ID, permits plain HTTP only for `localhost`, and otherwise expects production HTTPS. Password removal requires a stored passkey, an active recovery code, and a passkey verification fresh on the current session. Recovery rotates the recovery code and revokes existing sessions.
