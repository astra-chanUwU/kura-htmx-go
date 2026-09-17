# Kura

Kura is a fast, self-hostable image archive: the density and directness of a booru, with a cleaner browsing experience and a deliberately small operational footprint.

## Product principles

- Browsing is the main event. Search, tags, thumbnails, and keyboard navigation stay close at hand.
- Adding an image should take almost no effort, while publishing remains an explicit act.
- Server-rendered HTML is the default. HTMX is used only to replace useful page fragments.
- Browser dependencies are vendored; normal use does not depend on a public CDN.
- Complexity must purchase something. V1 is one Go process, SQLite, and media on disk.

## Current vertical slice

The current build includes public browsing/search, contextual previous/next navigation, local accounts with passwords and/or passkeys, controlled open/closed/invite-only registration, one-use recovery codes, private favorites, owned draft/published pools, moderator/admin uploads and metadata tools with categorized tag entry/autocomplete, account/role administration, graceful error pages, individual downloads, and bounded portable ZIP exports. Viewers add images to pools from a post or through a searchable thumbnail picker with visual removal and ordering—database IDs never need to be entered. Uploads are hash-named beneath the configured media root, retain the submitted basename as metadata, thumbnails are generated during the request, and duplicate files are rejected before metadata is created. Published or otherwise authorized posts can be downloaded with either a readable tagged filename or a safe original-basename filename; selected visible browse results and authorized pools can be exported with original JPEG, PNG, or GIF bytes plus a complete `manifest.json`.

Upload-capable accounts have a status-filtered **My uploads** view. An uploader can permanently delete their own post only after confirmation. An ordinary admin's cross-owner delete action quarantines the post instead; only the active super admin has server-wide image oversight and can inspect, restore, or permanently delete quarantined posts. Quarantined posts and media are excluded from normal browse, direct view, navigation, pools, favorites, downloads, and exports. Demoting a moderator or admin to viewer revokes their sessions and quarantines their non-deleted drafts; published uploads remain published, and promotion later does not restore quarantined drafts. Permanent deletion removes the database row, original, and thumbnail through private staging, rollback/reconciliation, and media-root containment, so the same SHA can be uploaded again.

See [docs/product.md](docs/product.md) for v1 scope and routes, and [docs/architecture.md](docs/architecture.md) for the architecture and data model.

## Run locally

Requires Go 1.25 or newer.

```sh
go mod download
go run ./cmd/kura -bootstrap-super-admin -seed
```

Open <http://localhost:8080>. The default database and generated demo images live in `./var/`. Override them with `-db`, `-media`, or `-addr`.

```sh
go test ./...
```

The command prints a one-use setup URL valid for 15 minutes. Open it to choose password-only, passkey-only, or both; no password is placed in an environment variable or terminal history. The flag refuses to run after a super admin exists, and ordinary redeployments never reopen setup. On later starts, omit the bootstrap flag. The server applies numbered embedded SQL migrations on startup. `-seed` is idempotent and adds generated local demo images; it never grants upload or admin permissions.

Passkeys default to RP ID `localhost` and origin `http://localhost:8080`. For production HTTPS, set an exact RP ID and comma-separated allowed origins before both bootstrap and normal starts:

```sh
KURA_RP_ID='kura.example.com' KURA_ORIGINS='https://kura.example.com' go run ./cmd/kura -bootstrap-super-admin
```

Registration is open by default. Set `KURA_REGISTRATION_MODE=closed` to keep existing accounts, login, recovery, and first setup available while disabling public registration. Set `KURA_REGISTRATION_MODE=invite-only` to let the active super admin issue expiring, single-use viewer invitations from Admin → Invitations; the local invite URL/code is shown once and can be revoked before use. The stored invite is only a SHA-256 hash. For example:

```sh
KURA_REGISTRATION_MODE=invite-only KURA_RP_ID=localhost KURA_ORIGINS=http://localhost:8080 go run ./cmd/kura -addr 127.0.0.1:8080
```

Passwords are 15–128 printable characters and are never trimmed or normalized. Usernames are 3–32 ASCII letters, digits, underscores, or hyphens, must begin/end alphanumerically, and compare case-insensitively. The account page manages named passkeys, password state, recovery-code replacement, and session revocation. A recovery code is shown only when generated or replaced; save it immediately in a password manager.

## Folder map

- `cmd/kura`: process entrypoint and demo seeding
- `internal/archive`: SQLite models and queries
- `internal/web`: HTTP handlers, templates, and static assets
- `internal/archive/migrations`: ordered schema changes embedded into the binary
- `var`: local runtime data (ignored by Git)

## Account roles

- Viewers can favorite published posts and create private-draft or public pools.
- Moderators can also upload JPEG, PNG, and GIF images, edit metadata, and permanently delete their own uploads after confirmation.
- Admins can upload and manage their own uploads, manage viewers and moderators, and quarantine cross-owner uploads instead of permanently deleting them.
- The single super admin additionally has server-wide image oversight, can review/restore/permanently delete quarantined posts, can promote/demote admins, and can explicitly transfer super-admin authority.

Sessions are stored in SQLite. Cookies are HTTP-only and SameSite=Lax; they are marked Secure automatically when Kura is served over TLS, while remaining usable on local plain HTTP. Every state-changing HTML form or JSON ceremony requires its session's CSRF token. Password login and recovery attempts have a small in-memory per-client/account rate limit.

## Bitwarden passkey acceptance checklist

Automated tests cannot prove browser-extension interoperability. In Chrome with Bitwarden enabled, manually check: multiple local usernames; consistent username identity across setup, registration, login, account, and recovery; account-aware editable passkey labels; passkey creation/storage; passkey-only registration; repeated sign-outs/sign-ins; labeled recovery-code copy/download and safe filenames; adding a second named passkey; fresh passkey verification followed by password removal; passwordless login; password and passkey recovery; cancelling each browser prompt; and Back/Forward behavior after inline account actions. Friendly labels help recognition but do not guarantee Bitwarden grouping.
