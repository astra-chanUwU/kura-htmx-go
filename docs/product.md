# Kura v1 product definition

## V1 features

- Browse a compact, responsive thumbnail grid.
- Search by tags and combine tags with space-separated AND semantics.
- Browse categorized tags: artist, character, copyright, general, and meta.
- Open post detail pages with image metadata, source, tags, pool membership, and previous/next keyboard navigation.
- Jump to a random published post.
- Browse pools/collections and their ordered posts.
- Favorite posts locally (single-owner installation; authentication can be added when needed).
- Upload from file, drag/drop, or clipboard through an admin screen.
- Preview before publish; save drafts and publish deliberately.
- Extract MIME type, dimensions, file size, and SHA-256; reject duplicates by hash.
- Generate thumbnails on disk.
- Autocomplete tags and surface recently used tags.
- Bulk-add or remove tags from a selected set when the basic upload flow is sound.

The first implemented slice is browsing: homepage, post index, categorized tag rail, filtering, pagination, post detail, pools, random navigation, schema, and local demo data. Upload/admin is the next slice.

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
| `GET /admin/uploads/new` | Future upload/draft screen |
| `POST /admin/uploads` | Future ingest and preview |
| `POST /admin/posts/{id}/publish` | Future publish action |

Query syntax in v1 is intentionally small: normalized tag names separated by spaces mean AND. A later syntax must earn its parsing and UI cost.
