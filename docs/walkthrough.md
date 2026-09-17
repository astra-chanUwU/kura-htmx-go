# Fresh-instance walkthrough

Findings and follow-up requests are recorded in [walkthrough-notes.md](walkthrough-notes.md).

Start with the one-use setup link printed by the local server. Use **localhost** consistently, including for passkeys. Seed account names, their shared local password, and the image/ownership map are in `var/walkthrough-accounts.md` (ignored by Git).

The starting dataset has five ordinary accounts: `viewer_one`, `viewer_two`, `moderator_one`, `moderator_two`, and `test_admin`. Create your own sixth account as the first super admin. There are 18 selected images: 16 published, two drafts; one public pool, two private pools; and different favorites for each viewer. Your source picture folder is untouched.

## 1. First setup

- [ ] Open the setup link and create your own username with a password, a passkey, or both. Passwords require 15–128 printable characters.
- [ ] Confirm your account is the super admin and can open account administration.
- [ ] If using a passkey, save the labeled Kura recovery details immediately. Check that Chrome/Bitwarden stores and uses the passkey; the friendly label helps identify the account but does not guarantee how items are grouped.
- [ ] Reopen the used setup link: it must not create another super admin.
- [ ] Sign out and sign back in with the method you chose.

Each setup method requires a separate fresh database to test first-time enrollment. This instance lets you choose one.

## 2. Anonymous browsing

- [ ] Sign out. Check home, the post grid, post details, and the public collection.
- [ ] Search `walkthrough`, `jpeg`, `png`, and `gif`; search `anime scene` to check that both tags are required. Try mixed case and extra spaces.
- [ ] Try a nonexistent tag and confirm a useful empty result.
- [ ] Open an image: check dimensions, size, tags, original image, and thumbnail. Check GIF animation on its detail page.
- [ ] Open an image from public browse/search, a pool, My uploads, and (as super admin) Admin → Images. Confirm each detail page shows validated Previous, Next, and Back controls for that source ordering; first/last images disable the unavailable direction.
- [ ] Use Left/Right Arrow outside controls to move through the current image context and Escape to return to its listing. While the quick editor is open, confirm typing in inputs/selects and pressing buttons does not navigate; modifier-key shortcuts do nothing. Use browser Back/Forward after moving between images.
- [ ] Confirm drafts #11 (moderator_two) and #16 (moderator_one) and both private draft pools cannot be opened while signed out. Admins can open draft image URLs directly; the browse grid does not list them. Private draft pools remain owner-only.
- [ ] Confirm favorites, upload, and administration require appropriate sign-in.
- [ ] Switch between the Light and Magic Girl themes; navigate and reload to check that the choice persists.
- [ ] Check a narrow/mobile browser window for usable images, forms, and navigation.

## 3. Viewer accounts and pools

- [ ] Sign in as `viewer_one`, add/remove a favorite, then open Favorites and remove one there.
- [ ] Sign in as `viewer_two`: its favorites must remain separate.
- [ ] Open each viewer's private draft pool as its owner; the other viewer must not see or edit it.
- [ ] Create a pool; use the thumbnail picker, tag search, and Favorites filter. Add, remove, and reorder images, then reload to confirm the order persists.
- [ ] Add a post to an owned pool from its detail page; repeat the add and check for duplicates.
- [ ] Publish a private pool and confirm it becomes visible while signed out.
- [ ] After favorite and pool membership changes, use Back/Forward: inline actions must not create extra history entries.
- [ ] Confirm viewers cannot upload, edit image metadata, or open account administration.

## 4. Upload and moderation

- [ ] As `moderator_one`, choose a new JPEG/PNG/GIF from the supplied folder that is absent from the seed manifest. Check its preview, then upload it with tags and a source URL.
- [ ] Repeat using drag/drop and clipboard paste where your browser supports them.
- [ ] Confirm the default Published selection makes the upload publicly visible. Upload another as Draft and check that signed-out visitors cannot access its page or media URL.
- [ ] Reupload an exact seeded original from the manifest: it must report a duplicate without adding a post.
- [ ] Try an unsupported file (for example a WebP or MP4 from the folder), an empty file, and a file exceeding 32 MB: each must be rejected.
- [ ] Edit tags/source through the inline editor; cancel, save, reload, and check search results. Publish a draft and verify public visibility.
- [ ] Use Back/Forward after inline editing and check it returns to the expected browsing page.
- [ ] As `moderator_one`, permanently delete its own disposable image through My uploads or its post action. Type `DELETE`, confirm the post row and both media files are gone, then re-upload the exact original to confirm the same SHA is accepted again. Confirm another moderator's image cannot be deleted by it.
- [ ] As `test_admin`, delete another account's disposable image. Confirm the ordinary-admin action quarantines it rather than permanently deleting it: it leaves normal browse/detail/media/navigation/pool/favorite/download/export paths, while the files remain available only for super-admin review.
- [ ] After adding nine more published images (25 total), check pagination and Back/Forward across pages and searches; opening an image from page 2 must return to the same search/page context.

## 5. Account security and administration

- [ ] Register a new viewer and check password sign-in, incorrect credentials, case-insensitive username matching, and duplicate usernames.
- [ ] On Account, change the password, confirm the read-only username identity, add the suggested account-specific passkey label (edit it if desired), sign out, and verify both sign-in methods.
- [ ] With Chrome/Bitwarden, use at least two local accounts. Check that setup, registration, login, account, and recovery username fields identify the intended account; test passkey-only registration and sign-in; add a second passkey to an existing account; cancel each browser prompt and confirm retry remains possible.
- [ ] After fresh passkey verification, remove the password and verify passwordless sign-in. Attempt to remove the last remaining sign-in method: it must be refused.
- [ ] Replace a recovery code and save the labeled/downloadable Kura details. Confirm the username, purpose, rotation warning, and account-specific filename; recover the account using it, save the replacement, and check that the old code cannot be reused.
- [ ] Sign in in two browser profiles; revoke other sessions and check that the other profile is signed out.
- [ ] As `test_admin`, promote/demote an ordinary viewer/moderator and suspend/reactivate an account. Confirm suspension blocks access while uploads remain owned and intact.
- [ ] Confirm `test_admin` cannot promote anyone to admin, demote an admin, or transfer super-admin authority.
- [ ] As your super admin, open Admin → Images. Check the draft/published/deleted/quarantined filters, uploader identities, thumbnails, pagination, and links to existing post/edit/review controls. Confirm ordinary admins do not see or open this view. Open a quarantined post, restore it to its prior status, then quarantine and permanently delete a disposable post by typing its post ID.
- [ ] Demote a disposable moderator/admin to viewer. Confirm its sessions are revoked, its non-deleted drafts are quarantined, published uploads remain published, and re-promoting the account does not auto-restore those drafts.
- [ ] As your super admin, promote/demote a disposable admin. Check protection against suspending/demoting the active super admin.
- [ ] As your super admin, open Admin → Audit. Filter readable role-change, suspension, super-admin-transfer, and permanent-deletion events; confirm actor/account/uploader identities remain visible after the affected post or account is gone, the final deletion snapshot is bounded and read-only, and ordinary admins receive `403`.
- [ ] If testing authority transfer, do it last: transfer to a disposable active admin and confirm only the recipient retains super-admin controls.
- [ ] Check Back/Forward after inline account and admin actions. With JavaScript disabled, check the ordinary favorite/pool/admin forms still submit and redirect.

### Chrome/Bitwarden identity checklist

- [ ] In separate Chrome profiles or clearly separated sessions, repeat the password and passkey flows for two usernames and verify the username shown beside each Kura credential before saving.
- [ ] Confirm passkey labels are editable and account-aware, then verify cancellation leaves the form usable and does not create a credential or rotate a recovery code.
- [ ] Copy the recovery details and download them once. Check that the copied/downloaded text names Kura, the username, the recovery purpose, and that replacement/recovery invalidates the previous code; check the filename contains only the safe account identifier.
- [ ] Treat browser-extension grouping, merging, and separation as manual observations only: the Kura labels are hints for recognition, not a guarantee of Bitwarden item behavior.

## Restarting later

After you finish setup, restart from the project directory with:

```sh
KURA_RP_ID=localhost KURA_ORIGINS=http://localhost:8080 go run ./cmd/kura -addr 127.0.0.1:8080
```

Do not add `-seed` to this walkthrough instance: it imports the separate embedded demo collection. Do not add the bootstrap flag after setup is complete.

If the initial setup link expires before you use it, stop this server and restart with `-bootstrap-super-admin` added to the command above. It prints a new link valid for 15 minutes and refuses to reopen setup after a super admin exists.
