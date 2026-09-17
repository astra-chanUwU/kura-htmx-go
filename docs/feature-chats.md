# Sequential feature chats

For each feature, create a new **local chat in the Kura project**, select **GPT-5.6 Luna** and **High** reasoning in the app, and paste that feature's complete prompt below. Prompt text does not itself select a model. Finish and review one feature before starting the next, so the next chat builds on the current checkout.

The chats have not been created automatically: computer control of Codex itself is blocked and the available app tools do not expose chat creation/model selection.

## Chat 1 — Server-wide image oversight

```text
Implement only server-wide image oversight in Kura at /Users/astrochan/Documents/Workstation/kura.

Read AGENTS.md and docs/walkthrough-notes.md. Inspect the current branch, remote, status, relevant handlers/templates/queries/tests before changes. Preserve existing walkthrough documents, unrelated changes, and all current users/database/media. Do not reset the instance, commit, or push.

The server owner needs to discover images uploaded by other accounts, including drafts. Currently draft detail/media access exists for upload-capable roles, but the public grid lists only published images. Draft #11 was seeded for moderator_two and #16 for moderator_one; verify the current database instead of assuming they remain unchanged.

Build a restrained image oversight view in the administration area, with draft/published filters, uploader filter/identity, thumbnails, pagination, and links to existing metadata/moderation controls. Keep private draft pools owner-only. Keep the public browse grid published-only. Clearly account for soft-deleted posts whose media remains on disk; this feature must not purge files or change deletion semantics.

Start with a concise design grounded in the existing UI. Settle whether this view is for super admin only or also ordinary admins before implementing that boundary. Do not silently change existing moderators' direct draft access as part of this feature. Use the existing Go/HTMX/SQLite/disk architecture and avoid speculative storage scanning, dependencies, dashboards, or unrelated features.

Use a few meaningful integration checks for the selected role boundary and filters. Run go test ./... and check the diff. Identify the actual running Kura server before restarting it; restart embedded assets from this checkout without -seed or bootstrap after setup. Smoke-check the actual HTTP UI, and manually check Back/Forward if interactive forms change. State exactly what was checked and what remains manual. Update walkthrough documents with the resulting behavior. Stop after this feature; do not begin My uploads or the other queued work.
```

## Chat 2 — My uploads

```text
Implement only My uploads in Kura at /Users/astrochan/Documents/Workstation/kura, after the server-wide image oversight feature has been completed and reviewed.

Read AGENTS.md and docs/walkthrough-notes.md, then inspect current Git state, the preceding feature's implementation, relevant code, and tests. Preserve existing documents, unrelated changes, and all runtime users/database/media. Do not reset, commit, or push.

Add a personal image listing for upload-capable users to discover and manage their own draft and published images. Provide status filters, thumbnails, pagination, and links to preview, edit, publish/unpublish, and delete using existing handlers and ownership rules. An ordinary user's personal listing must not include someone else's uploads; administration oversight remains a separate view. Keep private pools owner-only and public browsing published-only. Reuse preceding query/template work only where it keeps code simple.

Start with a concise design within Kura's existing text-first UI, then implement the agreed behavior. Keep Go/server-rendered templates/HTMX/SQLite/disk media. Use focused integration checks for ownership and draft visibility. Run go test ./... and diff checks. Identify and restart the actual local server for embedded changes without reseeding or reopening bootstrap. Verify the live UI; manually check Back/Forward when forms change. Update the walkthrough notes/checklist. Stop after My uploads; do not start keyboard or authentication work.
```

## Chat 3 — Keyboard and previous/next navigation

```text
Implement only keyboard and previous/next image navigation in Kura at /Users/astrochan/Documents/Workstation/kura, after the image listing features are complete and reviewed.

Read AGENTS.md and docs/walkthrough-notes.md. Inspect current Git state, relevant code/tests, and existing navigation contexts. Preserve other work and current database/media/users. Do not reset, commit, or push.

The current post-page shortcut sends Escape or Left Arrow to /posts and fires even inside editing fields; there is no real previous/next image navigation. Explore the intended behavior and present a concise design before implementation: visible previous/next controls, Left/Right Arrow navigation, an appropriate return action, preservation of search/pool order where practical, first/last boundaries, and no global shortcut interception inside inputs, textareas, selects, or editable content. Respect public, personal-upload, and admin draft visibility; navigation must not expose inaccessible posts.

Use simple server-rendered navigation and ordinary browser history. Avoid unrelated keyboard shortcuts or a client routing system. Test meaningful neighbor ordering/context and authorization boundaries. Run go test ./... and diff checks. Restart the actual server for embedded changes without reseeding/bootstrap. Manually verify shortcuts both outside and inside the quick editor, visible controls, boundary behavior, and Back/Forward. Update the walkthrough documents with the exact keys and contexts supported. Stop after this feature.
```

## Chat 4 — Password-manager defaults

```text
Implement only clearer passkey/password/recovery defaults in Kura at /Users/astrochan/Documents/Workstation/kura, after the prior features have been completed and reviewed.

Read AGENTS.md and docs/walkthrough-notes.md. Inspect current Git state, account/setup/registration/recovery templates, passkey scripts/handlers, and tests. Preserve all unrelated work and current runtime users/database/media. Do not reset, commit, or push.

The user wants distinguishable, consistent password-manager entries when combining a password, passkeys, and recovery codes, especially across multiple test users. Existing generic passkey labels include My passkey, Super admin passkey, and Recovered passkey. Investigate the actual Chrome/Bitwarden confusion before claiming a root cause; if reproduction requires the user's extension, arrange a focused manual walkthrough and continue independent inspection.

Present a concise design for account-aware, editable passkey defaults; consistent username/autofill identity across forms; and recovery-code copy/download content that identifies the account/site and explains saving recovery information with that account. Preserve current origin/RP ID, WebAuthn account handles, stored credentials, password policy, one-use recovery rotation, last-authenticator protection, and fresh verification requirements. Do not merge recovery codes into a password field or imply that friendly labels alone control password-manager item merging.

Implement scoped improvements using the existing architecture. Add only focused tests protecting affected authentication/recovery behavior; run go test ./... and diff checks. Identify/restart the actual local server for embedded changes without reseeding or bootstrap. Check the live forms. Real passkey/password-manager interoperability and credential entry may require the user; clearly report manual results and unverified cases. Check Back/Forward for changed interactive forms. Update walkthrough documents and stop after this feature.
```
