# Spec: GitHub Actions native bundle creation (#916)

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future — Lens 1: Kargo parity`
- **Implements**: No GitHub Actions native bundle creation (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

O1. The action at `.github/actions/create-bundle/action.yml` accepts a `image` input
    (single image reference as `repo:tag` or `repo@sha256:digest`) in addition to `images`
    (newline-separated list). When `image` is set and `images` is not, the action behaves
    identically to setting `images` with a single entry.

O2. The action outputs `bundle-status-url` (value: `${kardinal-url}/ui#pipeline=${pipeline}`)
    in addition to `bundle-name` and `bundle-namespace`.

O3. The curl call retries up to 3 times with exponential backoff (1s, 2s, 4s) on transient
    failures (exit code non-zero, HTTP 5xx). Permanent failures (HTTP 4xx) do not retry.

O4. `docs/ci-integration.md` example workflow uses `image:` input (matching the documented
    interface) and passes `yamllint .github/actions/create-bundle/action.yml` without errors.

O5. A shell-level test at `.github/actions/create-bundle/test.sh` exercises the image
    parsing logic (repo:tag, repo@digest, bare repo) without network calls and exits 0.
    CI runs this test in the GitHub Actions CI workflow.

---

## Zone 2 — Implementer's judgment

- Use `jq` or `python3` for JSON construction; prefer `python3` since it is already used
  in the existing action for response parsing.
- The status URL format `/ui#pipeline=<name>` must match the existing URL routing in the UI
  (hash-fragment routing per design doc 06).
- Retry loop: use a `for` loop with `sleep` — no external retry tools.
- The `image` input, when provided alongside `images`, should be prepended to the list.

---

## Zone 3 — Scoped out

- No port-forward discovery logic (that requires kubectl, which CI runners may not have
  in a generic setup). The `kardinal-url` input must be a reachable URL.
- No HMAC signature support in the action (HMAC is not currently in the bundle API).
- No bundle status polling / wait-for-verify mode (future enhancement).
