# Walkthrough findings — 2026-09-13

Recorded from the user's fresh-instance walkthrough. These are observations and requested follow-up work; application behavior has not been changed.

## Keyboard navigation needs exploration

The checklist overstated the current behavior: post pages have no previous/next image navigation. Their current shortcut sends Escape or Left Arrow to `/posts`; Right Arrow does nothing. The handler also runs while typing in form fields, which can interrupt metadata editing.

Explore expected shortcuts, visible previous/next controls, preserving the current search/pool context, and avoiding shortcuts while editing fields. Browser history and first/last-image behavior need manual acceptance when implemented.

## Draft image ownership and visibility

In this walkthrough seed:

| Draft image | Owner |
| --- | --- |
| #11 | moderator_two |
| #16 | moderator_one |

The user could not discover these drafts as super admin. The public browse grid always lists published images, including when signed in as an administrator. There is no personal upload listing or server-wide draft listing.

The current detail/media authorization already allows upload-capable accounts (moderators, admins, and super admin) to access another uploader's draft directly. Live HTTP checks confirmed the seeded ordinary admin could open `/posts/11` and `/posts/16` (200). The super-admin role follows the same handler permission path, but its specific browser session was not tested. Anonymous access was rejected during initial seed verification.

Thus the confirmed missing capability is discovering/managing drafts. If a direct draft URL also fails in the user's super-admin session, investigate that separately rather than assuming a role restriction.

## Private draft pools remain owner-only

The user accepts this behavior:

- `viewer-one-private-draft` belongs to viewer_one.
- `viewer-two-private-draft` belongs to viewer_two.
- Other viewers and administrators cannot view or edit these draft pools; publishing makes them browsable.

Live HTTP checks confirmed the ordinary admin received 404 for both private pools. Keep this collection privacy requirement distinct from oversight of the uploaded image files stored on the server.

## Requested: server-wide image oversight

The user wants the server owner to be aware of images stored by moderators, including unpublished images. Their concern is that a moderator could use drafts to keep objectionable or potentially illegal material on the owner's storage without the owner discovering it.

Requested follow-up: a separate image oversight view in the admin/super-admin area, with draft/published filters, uploader identity, thumbnails/details, and appropriate moderation controls. Super-admin access is required; whether ordinary admins share this view remains a design choice to settle. Account for the existing policy that deleting a post retains its original and thumbnail on disk: hiding a post is not removal from storage.

The current code also gives every moderator direct access to others' draft images. Review whether this remains the intended moderation boundary when designing the oversight view.

## Requested: my uploads

There is no panel for an uploader to review and manage their own drafted and published images. Add a personal image listing with status filters and links to preview, edit, publish/unpublish, and delete where their role/ownership permits. Keep the public browse grid focused on published images.

## Requested: clearer password-manager defaults

The user wants better defaults when combining passkeys, passwords, and recovery codes, so the entries are distinguishable and do not conflict in their password manager.

Observed defaults include `My passkey`, `Super admin passkey`, and `Recovered passkey`. Record the actual password-manager collision/replacement behavior during a Chrome/Bitwarden walkthrough before treating a particular default as the cause.

Explore clear account-specific passkey labels, consistent account/site identity across setup and account forms, and recovery-code copy/download text that identifies the account. Recovery codes should be saved as recovery information associated with the account, without presenting them as a replacement login password. Verify password autofill, adding a passkey to an existing password entry, recovery-code replacement, and multiple local test accounts. Browser-extension interoperability requires manual acceptance.

## Seed sign-in reference

Seeded usernames: viewer_one, viewer_two, moderator_one, moderator_two, test_admin. Their shared local test password is recorded in `var/walkthrough-accounts.md`, which is ignored by Git. User-created accounts use the credentials chosen during the walkthrough.

# Implemented: server-wide image oversight — 2026-09-13

The administration area now has a super-admin-only **Images** view at `/admin/images`. It lists image records across accounts, including drafts, with status filters for all, drafts, published, and deleted records; an uploader identity filter; thumbnails for non-deleted records; pagination; and links to the existing post and metadata-edit controls.

The current walkthrough database was checked before implementation: drafts #11 and #16 still belong to `moderator_two` and `moderator_one`, respectively. The view uses SQLite post rows and uploader identities only; it does not scan storage or remove files. Soft-deleted records are shown as `deleted` with “Media retained on disk,” and their media remains unavailable through the existing protected media route. Public browse remains published/non-deleted only, and private draft pools remain owner-only.

The boundary is intentionally super-admin-only: ordinary admins still have account administration but receive `403` for `/admin/images` and do not see its navigation link. Existing upload-capable roles retain their direct draft detail/media access.

## Implemented: My uploads — 2026-09-17

The personal image-management view is available at `/uploads` for active moderators and admins. It is backed by an archive-owned uploader query that reloads the actor's current role and suspension state, then selects only non-deleted posts whose permanent `uploader_id` matches that actor. It provides All, Draft, and Published filters, 24-item pagination, thumbnails, dimensions/type/size and filename metadata, preview/edit links, owner-scoped publish/unpublish controls, and the existing soft-delete ownership rules.

Status changes and deletion have ordinary form/redirect fallbacks and history-neutral HTMX fragment updates. The direct status command preserves source/tags and is authorized to the current owner at the archive boundary; moderators cannot use the personal endpoints to enumerate or mutate another uploader's images. Deleted media is excluded from the personal view and remains protected by the existing media visibility rules. Public browsing and private draft pools remain unchanged, and `/admin/images` remains the separate super-admin server-wide oversight view.
