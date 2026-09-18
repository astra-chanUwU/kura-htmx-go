# Browse Selection UX Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Kura's browse search, selection, export, and bulk-tag controls compact, contextual, responsive, and visibly stateful.

**Architecture:** Preserve the existing server-rendered templates and HTMX replacement boundary. Semantic structure lives in `posts.html`, selection state stays in the dependency-free `bulk-selection.js`, and presentation remains in `kura.css`; archive and route contracts do not change.

**Tech Stack:** Go `html/template`, HTMX 2.0.4, dependency-free browser JavaScript, CSS, SQLite-backed existing handlers

**Spec:** `docs/superpowers/specs/2026-09-18-browse-selection-ux-design.md`

## Global Constraints

- Keep Kura a Go modular monolith using server-rendered templates, HTMX, SQLite, and disk-backed media.
- Do not change routes, persistence, permissions, upload behavior, export contents, or tag parsing.
- Bulk tagging remains preview-first, atomic, and limited to 24 visible browse-page posts.
- Selected ZIP exports retain the existing 100-image and 512 MiB limits.
- Inline browse updates must not add or replace browser-history entries beyond the existing search and pagination behavior.
- Preserve ordinary HTML form submission and redirects as the existing progressive-enhancement fallback.
- Do not crop thumbnails; continue using contained originals/thumbnails.
- Add no dependencies or framework.
- Do not commit or push unless the user explicitly requests it.

---

### Task 1: Semantic browse and contextual-control markup

**Files:**
- Modify: `internal/web/templates/posts.html`
- Modify: `internal/web/bulk_tags_test.go`
- Modify: `internal/web/search_test.go`

**Interfaces:**
- Consumes: existing `.Query`, `.Sort`, `.Page`, `.User.CanUpload`, export limits, CSRF, and `data-*` hooks used by `bulk-selection.js`.
- Produces: `.search-query-row`, `.search-sort-row`, `[data-selection-status]`, `[data-selection-actions]`, `[data-bulk-editor]`, and `[data-tag-categories]` hooks for Tasks 2 and 3.

- [ ] **Step 1: Add failing render assertions**

  Extend focused web tests to render the browse page and assert:
  - the query input is in `.search-query-row` while sort and submit are in `.search-sort-row`;
  - the selection count is within an `aria-live="polite"` status region;
  - contextual actions are grouped by `[data-selection-actions]`;
  - bulk tag inputs are inside a native disclosure marked `[data-bulk-editor]` with visible text `Edit tags`;
  - the submit copy is `Review tag changes`;
  - tag categories are grouped beneath `[data-tag-categories]` with a `Filter by tags` disclosure label.

- [ ] **Step 2: Verify the focused tests fail for missing structure**

  Run: `go test ./internal/web -run 'TestBrowseSearchSortAndHTMXPreserveContext|TestBulkTagControlsRespectRole' -count=1`

  Expected: FAIL because the new semantic hooks and copy are absent.

- [ ] **Step 3: Restructure only the browse template**

  Keep every existing form action, method, input name, CSRF field, HTMX target, query parameter, limit value, checkbox hook, permission guard, and thumbnail link. Add the semantic wrappers from Step 1, move bulk tagging into a native `details` disclosure, and keep export actions in the selection workspace.

- [ ] **Step 4: Verify focused tests pass**

  Run: `go test ./internal/web -run 'TestBrowseSearchSortAndHTMXPreserveContext|TestBulkTagControlsRespectRole' -count=1`

  Expected: PASS with pristine output.

- [ ] **Step 5: Self-review the rendered contract**

  Confirm viewers still receive export selection but no bulk tag editor; upload-capable users receive both; all pre-existing `data-*` hooks referenced by `bulk-selection.js` remain present.

### Task 2: Contextual selection behavior

**Files:**
- Modify: `internal/web/static/bulk-selection.js`
- Modify: `internal/web/bulk_tags_test.go`

**Interfaces:**
- Consumes: Task 1 hooks `[data-selection-status]`, `[data-selection-actions]`, `[data-bulk-editor]`, existing `[data-export-post]`, `[data-bulk-post]`, `[data-selection-select-page]`, and `[data-selection-clear]`.
- Produces: `data-selected="true"` on selected `.bulk-card` elements, contextual hidden-state synchronization, and Shift-click range selection limited to current `boxes()` order.

- [ ] **Step 1: Add failing behavior-contract assertions**

  Extend the focused static-script response test to require stable markers for contextual synchronization and range selection, while retaining assertions for history restoration. Name the user-visible break each assertion catches: selected-card state missing after checkbox changes, actions exposed at zero selection, disclosure left open after clear, or Shift-click failing to select the contiguous visible range.

- [ ] **Step 2: Verify the focused test fails**

  Run: `go test ./internal/web -run TestBulkSelectionScriptResetsRestoredHistory -count=1`

  Expected: FAIL because contextual/range behavior is not present.

- [ ] **Step 3: Implement minimal state synchronization**

  In the existing IIFE, keep generated hidden inputs and limit checks unchanged. During every `sync()`:
  - set each card's `data-selected` to match its checkbox;
  - hide contextual actions and the bulk editor at zero selection and reveal them otherwise;
  - close the bulk disclosure when selection becomes empty;
  - retain selection count, bytes, disabled states, and history-reset behavior;
  - implement Shift-click range selection using the last directly changed checkbox and the current visible `boxes()` order.

- [ ] **Step 4: Verify focused tests pass**

  Run: `go test ./internal/web -run 'TestBulkSelectionScriptResetsRestoredHistory|TestBulkTagControlsRespectRole' -count=1`

  Expected: PASS with pristine output.

- [ ] **Step 5: Inspect integration risks**

  Confirm `bind()` remains idempotent after HTMX swaps, history restore still clears selection, no inline action gains `hx-push-url`, and viewers without bulk controls do not hit null references.

### Task 3: Responsive visual treatment

**Files:**
- Modify: `internal/web/static/kura.css`

**Interfaces:**
- Consumes: Task 1 semantic classes/hooks and Task 2 `data-selected="true"` state.
- Produces: repaired tag-rail search layout, compact sticky selection workspace, full-width tag fields, clear disabled styles, coherent thumbnail canvases, and narrow tag disclosure behavior.

- [ ] **Step 1: Record the pre-change visual failures**

  At a wide desktop viewport, confirm the query input collapses in the 190px rail, bulk inputs do not fill their columns, the review action is visually detached, and selected tiles lack a card-level state. At 375px, confirm tag categories precede and push down the results.

- [ ] **Step 2: Add the minimal CSS**

  - stack `.search-query-row` above `.search-sort-row` in the desktop rail;
  - make contextual controls compact and sticky without covering navigation;
  - make bulk tag inputs fill their grid tracks and place the review button adjacent to the fields;
  - give disabled buttons a clearly muted, non-interactive treatment;
  - add a subtle bordered thumbnail canvas and selected outline/background without cropping;
  - enlarge the checkbox label target to at least 32 by 32 CSS pixels;
  - at `max-width:700px`, keep search/sort visible and collapse tag categories inside the native disclosure.

- [ ] **Step 3: Run automated regression tests**

  Run: `go test ./internal/web -count=1`

  Expected: PASS with pristine output.

- [ ] **Step 4: Self-review both themes and viewports**

  Check light and dark themes at a wide desktop viewport and 375px. Confirm no horizontal overflow, no clipped query/tag text, selected state remains visible without relying only on color, and the sticky bar does not obscure the first grid row.

### Task 4: Integrated browser and regression verification

**Files:**
- Modify only if a verified defect requires a scoped correction: `internal/web/templates/posts.html`, `internal/web/static/bulk-selection.js`, `internal/web/static/kura.css`, and their focused tests.

**Interfaces:**
- Consumes: completed Tasks 1-3.
- Produces: evidence that the combined browse flow works across server rendering, JavaScript state, HTMX navigation, themes, and narrow layout.

- [ ] **Step 1: Run the full automated suite**

  Run: `go test ./...`

  Expected: every Go package passes with pristine output.

- [ ] **Step 2: Run the desktop smoke flow**

  At `http://localhost:8080/posts` in light theme, verify search, sort, one selection, multiple selection, Select this page, Clear, both ZIP buttons' enabled/disabled states, Edit tags disclosure, Add/Remove input sizing, and Review tag changes navigation.

- [ ] **Step 3: Verify history behavior**

  Navigate with HTMX search/pagination, use Back and Forward, and verify restored pages show unchecked boxes, zero selected, closed bulk disclosure, and no new history entry from inline selection changes.

- [ ] **Step 4: Run responsive/theme smoke checks**

  Repeat the structural checks at 375px and in dark theme. Confirm tag categories collapse on narrow screens, controls remain keyboard reachable, and the page has no horizontal overflow.

- [ ] **Step 5: Review the complete uncommitted diff**

  Confirm changes are limited to the approved browse UX, documentation, and focused tests; preserve unrelated user changes; make no commit or push.
