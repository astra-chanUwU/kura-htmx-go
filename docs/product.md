# Kura v1 product definition

## V1 features

- Browse a compact, responsive thumbnail grid.
- Search by tags and combine tags with space-separated AND semantics.
- Browse categorized tags: artist, character, copyright, general, and meta.
- Open post detail pages with image metadata, source, tags, pool membership, and previous/next keyboard navigation.
- Jump to a random published post.
- Browse pools/collections and their ordered posts.
- Register and sign in with password, passkey-only, or both; recover locally without email; keep favorites private to each viewer.
- Create owned pools as owner-only drafts or publicly visible collections; add from post pages or use a searchable thumbnail picker to select and reorder images.
- Upload from file, drag/drop, or clipboard as a moderator or admin.
- Preview before publish; save drafts and publish deliberately.
- Extract MIME type, dimensions, file size, and SHA-256; reject duplicates by hash.
- Generate thumbnails on disk.
- Moderate tags, sources, and draft/published state, including bounded bulk tag deltas with an explicit preview.
- Enforce upload ownership for self-service changes, ordinary-admin quarantine of cross-owner uploads, and super-admin-only admin role changes.
- Download selected images from the current browse page or an authorized pool as a portable ZIP containing original JPEG, PNG, or GIF bytes and `manifest.json`.
- Show upload-capable users their own non-deleted uploads with draft/published status management.
- Provide super-admin-only server-wide image oversight, quarantine review, restoration, and permanent deletion.
- Keep an immutable super-admin audit history for account role/suspension changes, super-admin transfer, moderation actions, metadata edits, and permanent deletion snapshots.
- Present graceful user-facing error pages, contextual previous/next navigation, and account-aware passkey/password/recovery defaults.

The implemented vertical slice covers browsing, identity, role boundaries, uploads, categorized tag entry with bounded editor-only autocomplete, favorites, pool visibility, account administration, My uploads, super-admin image oversight, quarantine/review/permanent deletion, immutable super-admin audit history, contextual navigation, graceful errors, account-aware authentication defaults, a 24-post editor-only bulk tag preview/apply flow, and a 100-image/512 MiB portable ZIP export. Export selection is limited to the currently visible browse/search page; pool exports preserve their stored order. Export access does not grant metadata-edit authority.

## Explicit non-goals

- Manga library, torrents, blogging, or chat.
- Recommendations or algorithmic feeds.
- Federation or multi-node storage.
- General-purpose file management.
- A single-page application or client-side state framework.
- Premature object storage, queues, search services, or microservices.

## Routes

| Route | Purpose |
| --- | --- |
| `GET /` | Sparse home, primary search, navigation |
| `GET /posts?q=tag+tag&page=1` | Published post grid and filters |
| `GET /posts/grid?...` | HTMX grid/rail/pagination fragment |
| `GET /posts/export?post_ids=...&name=tagged\|original` | Download a bounded selected-image ZIP |
| `POST /posts/bulk-tags/preview` | Preview bounded add/remove tag deltas for selected posts |
| `POST /posts/bulk-tags/apply` | Apply a freshly authorized bounded tag delta |
| `GET /posts/{id}` | Post detail |
| `GET /random` | Redirect to a random published post |
| `GET /pools` | Pool index |
| `GET /pools/{slug}` | Ordered posts in a pool |
| `GET /pools/{slug}/export?name=tagged\|original` | Download an authorized pool ZIP |
| `GET/POST /register`, `GET/POST /login`, `GET/POST /recover` | Password account access and recovery |
| `POST /auth/passkeys/...` | Passkey registration, discoverable login, and fresh verification ceremonies |
| `GET /account` and `POST /account/...` | Favorites, pools, passkeys, password/recovery state, and sessions |
| `GET /setup`, `POST /setup/...` | Expiring one-use first-super-admin enrollment |
| `GET /pools/new`, `POST /pools` | Create a draft or published pool |
| `GET/POST /pools/{slug}/edit` | Owner-only pool editing |
| `GET /pools/picker` | HTMX thumbnail search for the visual pool editor |
| `POST /posts/{id}/pools` | Add the current post to an owned pool |
| `GET /uploads/new`, `POST /uploads` | Moderator image ingestion |
| `GET/POST /posts/{id}/edit` | Moderator metadata and publication state |
| `POST /posts/{id}/delete` | Permanently delete the current user's upload after confirmation, or quarantine a cross-owner upload when used by an ordinary admin |
| `GET /admin/accounts` | Admin account and role management |
| `GET /uploads` | The current moderator/admin's non-deleted uploads with all/draft/published filters and pagination |
| `POST /uploads/{id}/status` | Owner-scoped publish/unpublish status change |
| `POST /uploads/{id}/permanent-delete` | Confirmation-gated permanent deletion of an owned upload |
| `GET /admin/images` | Super-admin-only server-wide image oversight with all/draft/published/deleted/quarantined filters and uploader filtering |
| `GET /admin/images/{id}/review` | Super-admin review of a quarantined post |
| `GET /admin/audit` | Super-admin-only immutable audit history with action, actor, and post filters |
| `POST /admin/images/{id}/quarantine` | Super-admin quarantine action |
| `POST /admin/images/{id}/restore` | Super-admin restore to the pre-quarantine draft/published state |
| `POST /admin/images/{id}/permanent-delete` | Confirmation-gated super-admin permanent deletion |

Query syntax in v1 is intentionally small: normalized tag names separated by spaces mean AND. A later syntax must earn its parsing and UI cost.
