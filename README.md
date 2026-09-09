# Kura

Kura is a fast, self-hostable image archive: the density and directness of a booru, with a cleaner browsing experience and a deliberately small operational footprint.

## Product principles

- Browsing is the main event. Search, tags, thumbnails, and keyboard navigation stay close at hand.
- Adding an image should take almost no effort, while publishing remains an explicit act.
- Server-rendered HTML is the default. HTMX is used only to replace useful page fragments.
- Browser dependencies are vendored; normal use does not depend on a public CDN.
- Complexity must purchase something. V1 is one Go process, SQLite, and media on disk.

## Current vertical slice

The current build includes public browsing/search, local accounts, private favorites, owned draft/published pools, moderator uploads and metadata tools, and account/role administration. Viewers add images to pools from a post or through a searchable thumbnail picker with visual removal and ordering—database IDs never need to be entered. Uploads are hash-named beneath the configured media root, thumbnails are generated during the request, and duplicate files are rejected before metadata is created. Deleting a post hides its metadata but deliberately retains its files for explicit maintenance.

See [docs/product.md](docs/product.md) for v1 scope and routes, and [docs/architecture.md](docs/architecture.md) for the architecture and data model.

## Run locally

Requires Go 1.23 or newer.

```sh
go mod download
KURA_BOOTSTRAP_PASSWORD='use-a-long-password' go run ./cmd/kura -bootstrap-super-admin yourname -seed
```

Open <http://localhost:8080>. The default database and generated demo images live in `./var/`. Override them with `-db`, `-media`, or `-addr`.

```sh
go test ./...
```

The bootstrap flag is a one-time operation and refuses to replace an existing super admin. On later starts, omit the environment variable and bootstrap flag. The server applies numbered embedded SQL migrations on startup. `-seed` is idempotent and adds generated local demo images; it never grants upload or admin permissions.

## Folder map

- `cmd/kura`: process entrypoint and demo seeding
- `internal/archive`: SQLite models and queries
- `internal/web`: HTTP handlers, templates, and static assets
- `internal/archive/migrations`: ordered schema changes embedded into the binary
- `var`: local runtime data (ignored by Git)

## Account roles

- Viewers can favorite published posts and create private-draft or public pools.
- Moderators can also upload JPEG, PNG, and GIF images, edit metadata, and delete their own uploads.
- Admins can delete any upload and manage viewers and moderators.
- The single super admin can additionally promote/demote admins and explicitly transfer super-admin authority.

Sessions are stored in SQLite. Cookies are HTTP-only and SameSite=Lax; they are marked Secure automatically when Kura is served over TLS, while remaining usable on local plain HTTP. Every state-changing HTML form requires its session's CSRF token.
