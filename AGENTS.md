# Kura agent guidance

Kura is a small, self-hosted image board developed on a local machine. Favor clear, maintainable code and proportional verification. Do not spend the user's limited model budget on enterprise architecture, speculative hardening, or ceremonial work that does not protect a real project requirement.

## Architecture boundaries

- Keep Kura a Go modular monolith using server-rendered templates, HTMX, SQLite, and disk-backed media.
- Store uploaded originals and generated thumbnails beneath the configured media root. Never place user uploads in the Git repository.
- Store media paths relative to the media root in SQLite so an archive can move between disks without rewriting records.
- Do not add object storage, queues, microservices, OAuth, email verification, distributed systems, or deployment infrastructure unless the user explicitly requests them for a demonstrated need.
- Preserve the focused Safebooru-inspired image-board scope. Do not expand Kura into a generic file manager or social platform.

## Permission model

- Anonymous visitors can browse and search images and view published pools.
- Registered viewers can also maintain private favorites and create, edit, and publish their own pools.
- Moderators have viewer capabilities, can upload images and moderate metadata, and can delete their own uploads.
- Admins can delete any upload and manage ordinary accounts and moderator roles.
- Only the super admin can promote a viewer or moderator to admin, demote an admin, or transfer super-admin authority.
- Record upload ownership permanently. Account suspension or deletion must not silently orphan or remove media.
- Treat authorization, session security, CSRF protection, upload validation, and safe media writes as substantive requirements even for local development.

## Proportional testing

- Prefer a small number of integration tests covering real behavior over many narrow tests or extensive mocking.
- Test the boundaries where a defect could expose private state, cross roles, corrupt metadata, or lose media: authorization, ownership, upload validation, duplicate detection, and filesystem/database consistency.
- Skip trivial getter tests, exhaustive template assertions, redundant permutations, speculative load tests, and tests that merely restate implementation details.
- Run `go test ./...` after relevant changes and perform a focused manual smoke check for user-facing flows when practical.
- Do not claim a behavior was visually or manually verified unless that check was actually performed.

## HTMX and navigation behavior

- Use full-page navigation when a successful action intentionally leads to a different page.
- Use HTMX fragment updates for frequent inline mutations such as favorites, pool membership, and admin row actions.
- Inline mutations must not add or replace browser-history entries; do not use `hx-push-url` for them.
- Preserve ordinary HTML form submission and redirects as a progressive-enhancement fallback.
- When changing interactive forms, manually verify Back and Forward behavior and report whether that check was performed.
- Where both paths exist, focused handler tests should cover the HTMX fragment response and retain the non-HTMX redirect fallback.

## Working style

- Inspect the current checkout, Git status, relevant code, and existing tests before editing.
- Preserve unrelated user changes and keep diffs scoped to the request.
- Use simple defaults and make reasonable decisions without repeatedly asking about low-consequence details. Ask only when ambiguity would materially change behavior, data safety, or scope.
- Avoid unnecessary planning documents, abstractions, dependencies, and refactors. Complexity must purchase a concrete capability.
- Do not commit or push unless the user explicitly asks.
