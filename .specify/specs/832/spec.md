# Spec: feat(ci): GH_TOKEN scope preflight (issue #832)

## Design reference
- **Design doc**: `docs/design/13-scheduled-execution.md`
- **Section**: `§ Future` (was ✅ Present in PR #836, then removed by PR #845 which rewrote the workflow)
- **Implements**: Token expiry/scope preflight (🔲 → ✅)

## Zone 1 — Obligations

- O1: The `Validate GH_TOKEN` step in `otherness-scheduled.yml` must check that the token has `repo` and `workflow` OAuth scopes via `X-OAuth-Scopes` header.
- O2: If scopes are missing, the step must post a `[NEEDS HUMAN]` issue using GITHUB_TOKEN (not GH_TOKEN) and exit 1.
- O3: The scope check must NOT break the workflow when the token has the correct scopes (exit 0).
- O4: Must not touch GITHUB_TOKEN or OIDC/AWS credentials (O1 from issue #832).
- O5: Duplicate issue prevention — check for existing open `[NEEDS HUMAN] GH_TOKEN` issue before creating.

## Zone 2 — Implementer's judgment

- Enhance the existing `Validate GH_TOKEN` step (line 69) to add scope checking after the token validity check.
- Use `gh api /user -i` to get headers and grep for scopes.

## Zone 3 — Scoped out

- No change to OIDC or AWS credentials.
- No change to GITHUB_TOKEN handling.
