# Completed feature slices

This file is a historical record of the focused Kura slices that were implemented in sequence. The prompts that originally queued work have been retired; the behavior below is current implementation, not a to-do list.

## Server-wide image oversight — completed

Commit `c0ea01` added the restrained `/admin/images` view. It is available only to the active super admin and supports all, draft, published, deleted, and quarantined filters, uploader filtering, pagination, thumbnails where media is available, and links into existing post/edit/review controls. Ordinary admins retain account administration but receive `403` for this view.

## My uploads — completed

Commit `df8eab8` added `/uploads` for active moderators and admins. The archive query reloads the current actor and returns only that account's non-deleted, non-quarantined uploads. All, draft, and published filters, pagination, preview/edit links, owner-scoped publish/unpublish, and confirmation-gated permanent deletion are supported; status changes also have history-neutral HTMX fragments, while deletion retains an ordinary confirmation-and-redirect fallback.

## Contextual previous/next navigation — completed

Commit `e1f785a` added visible Previous, Next, and Back controls plus Left/Right Arrow and Escape handling. Allow-listed contexts cover public browse/search, ordered pools, My uploads, and super-admin image oversight. Neighbors are recomputed for the current actor; inaccessible, deleted, private, and quarantined posts are excluded, and invalid contexts fall back to `/posts`. Editable controls and modifier-key combinations do not trigger shortcuts.

## Account-aware authentication defaults — completed

Commit `7084cb5` aligned username/autocomplete identity across setup, registration, login, account, and recovery forms; made passkey defaults account-aware and editable; and labeled recovery output/downloads with the site, username, purpose, rotation warning, and safe filename. These labels aid recognition but do not promise password-manager grouping. Password policy, WebAuthn origin/RP checks, recovery rotation, and last-authenticator protections remain unchanged.

## Quarantine and permanent deletion — completed

Commit `eaa1a54` replaced the old logical-delete documentation/behavior boundary with explicit lifecycle handling. An owner's confirmed delete permanently removes the post row, original, and thumbnail. An ordinary admin's cross-owner delete quarantines the post and preserves it for review. The active super admin can review quarantined posts, restore their recorded draft/published state, or permanently delete them. Demoting a moderator/admin to viewer revokes sessions and quarantines that user's non-deleted drafts; published uploads remain published and re-promotion does not auto-restore. Private staging, rollback, startup reconciliation, and media-root containment protect the filesystem transaction, and normal browse/detail/media/navigation/pools/favorites/downloads/exports exclude quarantined posts.
