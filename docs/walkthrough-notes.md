# Walkthrough notes — current behavior, 2026-09-17

The original fresh-instance findings and their focused follow-up slices are recorded here. The implementation status below is current; runtime credentials and seeded data remain checkout-local.

## Draft image ownership and visibility

In the original walkthrough seed:

| Draft image | Owner |
| --- | --- |
| #11 | moderator_two |
| #16 | moderator_one |

The public browse grid lists only published, non-deleted, non-quarantined images. Active upload-capable accounts can open non-quarantined drafts directly for moderation; anonymous visitors cannot. Quarantined posts are excluded from normal detail and media authorization for every ordinary role, while an active super admin inspects them through the review view.

Private draft pools remain owner-only. `viewer-one-private-draft` belongs to `viewer_one`, and `viewer-two-private-draft` belongs to `viewer_two`; other viewers and administrators cannot view or edit them until publication.

## Implemented: server-wide image oversight

The super-admin-only `/admin/images` view lists all post records with all, draft, published, deleted, and quarantined filters, uploader filtering, pagination, available thumbnails, and links to existing moderation/review controls. Ordinary admins receive `403` and do not see its navigation link. The view is metadata-backed; it does not scan arbitrary storage.

An ordinary admin's cross-owner delete action quarantines rather than permanently deleting. The active super admin can inspect a quarantined post at `/admin/images/{id}/review`, restore it to its recorded previous status, or permanently delete it after typing the post ID. Quarantined media is retained for review but is unavailable through normal browse, post, media, navigation, pool, favorite, download, and ZIP-export paths.

## Implemented: My uploads

`/uploads` is available to active moderators and admins. It is strictly uploader-scoped, excludes deleted and quarantined posts, provides all/draft/published filters and 24-item pagination, and links to preview/edit plus owner-scoped publish/unpublish actions. An owner can permanently delete an upload only after typing `DELETE`; the operation removes metadata, original, and thumbnail.

Status changes have ordinary redirect fallbacks and history-neutral HTMX fragment updates; permanent deletion uses an ordinary confirmation form and redirect. The archive boundary reloads the actor's current role, suspension state, and ownership, so a stale or suspended session cannot enumerate or mutate uploads.

## Implemented: contextual post navigation

Post detail pages expose visible Previous, Next, and Back controls. Listing links carry a small allow-listed context for public browse/search, ordered pools, My uploads with its status filter, and super-admin image oversight with its status/uploader filters. The archive recomputes neighbors from that context for the current actor; it does not trust a return URL or client-supplied neighboring IDs. Invalid or no-longer-authorized contexts fall back to `/posts`, and deleted/private/inaccessible/quarantined records are excluded.

Escape follows the validated Back link, Left Arrow/Right Arrow follow available neighbors, and the shortcuts ignore inputs, textareas, selects, buttons, contenteditable content, and modifier keys. The controls use ordinary links so browser history remains authoritative.

## Implemented: role demotion and quarantine

When an authorized admin demotes a moderator/admin to viewer, Kura revokes that account's sessions and quarantines its non-deleted drafts in the same archive transaction. Published uploads remain published. Re-promoting the account does not automatically restore quarantined drafts; a super admin must review and restore them explicitly.

## Implemented: permanent deletion and safe media lifecycle

Permanent deletion is confirmation-gated and removes the SQLite post row, original, and thumbnail. Media is first moved into a private staging directory beneath the configured media root, with rollback on failure and startup reconciliation for interrupted staging. Relative-path checks prevent escaping the configured root, and once metadata/media are gone the same SHA can be uploaded again.

## Implemented: graceful errors and authentication defaults

User-facing failures use styled HTML pages or safe HTMX/JSON responses appropriate to the request. Setup, registration, login, account, and recovery forms share account-aware username/autocomplete identity; passkey defaults are editable and account-aware; recovery output names Kura, the username, purpose, rotation warning, and safe download filename. These labels help recognition but do not guarantee password-manager grouping. WebAuthn origin/RP checks, password policy, recovery rotation, and fresh-passkey requirements remain unchanged.

## Seed sign-in reference

Seeded usernames: `viewer_one`, `viewer_two`, `moderator_one`, `moderator_two`, `test_admin`. Their shared local test password is recorded in `var/walkthrough-accounts.md`, which is ignored by Git. User-created accounts use the credentials chosen during the walkthrough.
