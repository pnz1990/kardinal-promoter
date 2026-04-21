# Spec 917: Create Bundle Dialog in the UI

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future — Lens 1: Kargo parity`
- **Implements**: "No UI for Bundle creation / triggering promotions" (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable, must satisfy)

**O1** — A "Create Bundle" button appears in the pipeline header (ActionBar area) for each
pipeline. Clicking it opens a dialog modal with `role="dialog"` and `aria-modal="true"`.

**O2** — The dialog contains:
- A required "Image" text input (`id="bundle-image"`) for a container image reference
  (e.g. `ghcr.io/example/app:sha-abc1234`). Empty string must be rejected on submit.
- An optional "Commit SHA" text input (`id="bundle-commit-sha"`).
- An optional "Author" text input (`id="bundle-author"`).
- A "Create Bundle" submit button and a "Cancel" button.

**O3** — On submit with a non-empty image, the dialog calls `POST /api/v1/ui/bundles`
with body `{"pipeline": <name>, "image": <image>, "commitSHA": <sha>, "author": <author>, "namespace": <ns>}`.
The backend creates a Bundle CRD with `spec.type="image"`, `spec.images=[{repository: ..., tag: ...}]`
(or digest if the reference contains `@`), and `spec.provenance.{commitSHA, author}` if provided.

**O4** — On success (HTTP 201), the dialog closes and `onRefresh()` is called to re-poll.
A success message is NOT shown in the dialog (the refresh will show the new bundle in the timeline).

**O5** — On error, the dialog stays open and displays the error inline with `role="alert"`.
The "Create Bundle" button reverts from "Creating…" to "Create Bundle".

**O6** — While loading, both the "Create Bundle" and "Cancel" buttons are `disabled`.

**O7** — The backend endpoint `POST /api/v1/ui/bundles` is registered on the UI mux alongside
`/promote`, `/rollback`, `/pause`, `/resume`. It requires the same auth as other `/api/v1/ui/*`
routes (auth middleware already applied at the mux level — no per-handler auth needed).

**O8** — The `CreateBundleDialog` and `CreateBundleButton` components have unit tests covering:
- Renders without crashing
- Submit with empty image shows validation error, does NOT call the API
- Submit with valid image calls `api.createBundle` once
- Error from API is displayed inline
- onRefresh is called after successful creation

---

## Zone 2 — Implementer's judgment

- Image parsing (repository/tag split): split on the last `:` for tag; use `@sha256:` prefix
  detection for digest. The backend already handles ImageRef parsing — keep frontend simple: 
  pass the full image string to the backend as `image` and let the backend parse it.
- The backend should re-use `sanitizeName` and the same Bundle-generation logic already in
  `handlePromote` (no new name-generation logic needed).
- Dialog placement: render via React portal or fixed overlay (same as ConfirmDialog pattern).
- The "Create Bundle" button in the header is a new secondary button styled to match the
  existing Pause/Resume button style.

---

## Zone 3 — Scoped out

- No multi-image support in this PR (the dialog accepts one image; multi-image bundles
  can be created via CLI or the existing POST /api/v1/bundles).
- No environment targeting (target environment is not required — the Bundle flows through
  all environments as per normal pipeline behavior).
- No rate limiting in the UI endpoint (the existing token auth and controller-level admission
  is sufficient for the UI path).
- No dry-run / preview mode.
