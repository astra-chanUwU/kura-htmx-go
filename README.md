# Kura

Kura is a fast, self-hostable image archive: the density and directness of a booru, with a cleaner browsing experience and a deliberately small operational footprint.

## Product principles

- Browsing is the main event. Search, tags, thumbnails, and keyboard navigation stay close at hand.
- Adding an image should take almost no effort, while publishing remains an explicit act.
- Server-rendered HTML is the default. HTMX is used only to replace useful page fragments.
- Browser dependencies are vendored; normal use does not depend on a public CDN.
- Complexity must purchase something. V1 is one Go process, SQLite, and media on disk.

## Current vertical slice

The current build includes a sparse homepage, dense post index, categorized tag rail, tag filtering, pagination, random-post navigation, post detail pages, pools, local demo data, and focused query tests. The schema also establishes drafts, sources, hashes, favorites, pools, and tag categories so upload/admin work can land without reshaping the browsing model.

See [docs/product.md](docs/product.md) for v1 scope and routes, and [docs/architecture.md](docs/architecture.md) for the architecture and data model.

## Run locally

Requires Go 1.23 or newer.

```sh
go mod download
go run ./cmd/kura -seed
```

Open <http://localhost:8080>. The default database and generated demo images live in `./var/`. Override them with `-db`, `-media`, or `-addr`.

```sh
go test ./...
```

The server applies numbered embedded SQL migrations on startup. `-seed` is idempotent and adds a small set of generated local demo images only when the archive is empty.

## Folder map

- `cmd/kura`: process entrypoint and demo seeding
- `internal/archive`: SQLite models and queries
- `internal/web`: HTTP handlers, templates, and static assets
- `internal/archive/migrations`: ordered schema changes embedded into the binary
- `var`: local runtime data (ignored by Git)

## Next practical slice

Build the upload/admin flow: drag/drop and paste, preview, MIME/dimension/SHA-256 extraction, duplicate warning, tag autocomplete/recent tags, thumbnail generation, save draft, and publish. JPEG/PNG/WebP decoding should be selected intentionally when that work starts; the demo seed currently generates local PNG originals and thumbnails.
