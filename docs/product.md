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
- Moderate tags, sources, and draft/published state.
- Enforce moderator upload ownership, admin deletion, and super-admin-only admin role changes.

The implemented vertical slice covers browsing, identity, role boundaries, uploads, categorized tag entry with bounded editor-only autocomplete, favorites, pool visibility, and account administration. Bulk tagging remains out of scope.

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
| `GET /posts/{id}` | Post detail |
| `GET /random` | Redirect to a random published post |
| `GET /pools` | Pool index |
| `GET /pools/{slug}` | Ordered posts in a pool |
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
| `POST /posts/{id}/delete` | Ownership-aware logical deletion |
| `GET /admin/accounts` | Admin account and role management |

Query syntax in v1 is intentionally small: normalized tag names separated by spaces mean AND. A later syntax must earn its parsing and UI cost.
