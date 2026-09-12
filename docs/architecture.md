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
- `posts`: lifecycle (`draft` or `published`), permanent uploader attribution, logical deletion, original and thumbnail paths, MIME, dimensions, byte size, SHA-256, source, timestamps.
- `tags`: unique normalized name, display name, and fixed category (`artist`, `character`, `copyright`, `general`, `meta`).
- `post_tags`: many-to-many post/tag assignment with assignment time.
- `pools`: owner, draft/published visibility, stable slug, name, and description.
- `pool_posts`: ordered many-to-many pool membership.
- `favorites`: private user-to-post relationship.
- `schema_migrations`: applied migration versions.

SQLite foreign keys are enabled. Hashes are unique. Tag names and pool slugs are unique. Published-time and join indexes support the first browsing paths.

## Filesystem contract

The database stores paths relative to the media root, never arbitrary absolute paths. Real uploads use `originals/YYYY/MM/<sha256>.<ext>` and `thumbs/YYYY/MM/<sha256>.jpg`; demo data uses `demo/` beneath those roots. Ingestion streams to a private temporary directory, validates byte size, MIME and decoded dimensions, creates the thumbnail, and atomically renames both files into place. The media routes resolve only paths referenced by a non-deleted post visible to the current requester, and use non-storing cache headers so publication and authorization checks run again for later requests. Database deletion is logical and never silently deletes media; filesystem reconciliation remains an explicit maintenance operation.

Account suspension deletes that account's active sessions but does not delete the account row. Upload ownership uses a restrictive foreign key, so an uploader account cannot be removed and silently orphan its permanent attribution. Legacy/demo posts may have no owner; every real upload records one.

WebAuthn verification is delegated to the maintained `github.com/go-webauthn/webauthn` library. Kura requires discoverable credentials and user verification, validates configured origins against the RP ID, permits plain HTTP only for `localhost`, and otherwise expects production HTTPS. Password removal requires a stored passkey, an active recovery code, and a passkey verification fresh on the current session. Recovery rotates the recovery code and revokes existing sessions.
