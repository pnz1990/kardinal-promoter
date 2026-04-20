# Spec: 832-token-preflight

> feat(ci): GH_TOKEN scope preflight — detect expired token before agent run

## Design reference
- **Design doc**: `docs/design/13-scheduled-execution.md`
- **Section**: `§ Future`
- **Implements**: Token expiry preflight (🔲 → ✅)

---

## Zone 1 — Obligations

**O1**: A workflow step named `Verify GH_TOKEN scopes` MUST execute before `Run otherness`.
- Violation: no preflight step, or it runs after `Run otherness`.

**O2**: The step MUST check that `GH_TOKEN` has `repo` and `workflow` scopes.
- Mechanism: `gh auth status` output; if either scope is absent, the step must call
  `gh issue create` to post a `[NEEDS HUMAN]` issue on report issue #1, then `exit 1`
  to fail the workflow run visibly.
- Violation: expired/missing-scope token does not produce a GitHub issue.

**O3**: If `GH_TOKEN` is valid with required scopes, the step MUST exit 0 (no side effects).
- Violation: preflight fails on a valid token.

**O4**: The preflight step MUST use `GH_TOKEN` from the secrets, not `GITHUB_TOKEN`.
- Violation: using `GITHUB_TOKEN` in the preflight step env.

---

## Zone 2 — Implementer's judgment

- Whether to use `gh auth status` or `gh api user` to detect expiry.
- How to identify the report issue number (hardcode 1, or read from `otherness-config.yaml`).
- Whether to create a new issue each time or check for an existing open issue with the same title.

---

## Zone 3 — Scoped out

- Automatic token rotation (PATs are long-lived; rotation is a manual operation).
- Checking scopes beyond `repo` and `workflow`.
- Any changes to Go code, CRDs, or UI.
