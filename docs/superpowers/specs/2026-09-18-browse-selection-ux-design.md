# Browse Selection UX Design

## Goal

Make Kura's browse, selection, export, and bulk-tag controls easier to scan and operate without changing archive rules, permissions, routes, or the restrained image-board character.

## Scope

- Repair the desktop tag-rail search layout so the query field cannot collapse beside sort controls.
- Keep the selection summary compact and make actions contextual to the current selection.
- Put bulk tagging behind an explicit `Edit tags` disclosure and retain review-before-apply.
- Give selected thumbnails an obvious non-color-only state and a larger checkbox target.
- Make thumbnail canvases visually coherent without cropping originals.
- Collapse the long tag list behind a disclosure on narrow screens while leaving search and sort available.
- Add Shift-click contiguous range selection within the visible page if it remains small and dependency-free.

## Interaction design

The browse page starts with a compact selection row. With no selection it offers `Select this page`; download, clear, byte total, and bulk-edit controls are hidden or visibly unavailable. Selecting a post reveals a sticky contextual row with the selected count, byte total, download actions, `Edit tags`, and `Clear`.

`Edit tags` opens a native disclosure containing full-width Add and Remove fields followed by `Review tag changes`. The server's existing preview page remains the mandatory confirmation step. Clearing the selection closes the disclosure.

Each selected card receives a visible outline/background treatment in addition to the native checked control. The thumbnail remains `object-fit: contain`; no image is cropped. Shift-click extends selection from the last explicitly changed checkbox to the current checkbox, bounded to the current rendered page.

The tag rail keeps the query field on its own row. Sort and submit controls sit below it on desktop. On narrow screens, search and sort remain visible while tag categories live in a native disclosure labelled `Filter by tags`.

## Boundaries

- No route, database, archive, permission, upload, export, or tag parsing changes.
- Bulk tagging remains limited to 24 visible browse-page posts and remains preview-first and atomic.
- Selected ZIP export keeps the existing 100-image and 512 MiB server limits.
- Existing HTMX browse replacement and Back/Forward selection reset behavior remain intact.
- Ordinary HTML navigation and form submission remain available where they exist today.
- No framework or dependency is added.
- No commit or push is made unless the user explicitly requests it.

## Verification

- Focused Go handler/render tests for semantic markup and unchanged authorization exposure.
- Full `go test ./...` after integration.
- Browser smoke checks at wide desktop and 375 px, in light and dark themes.
- Manual selection checks: single select, multiple select, select page, clear, Shift-click range, export actions, bulk tag disclosure, preview navigation, and Back/Forward behavior.
