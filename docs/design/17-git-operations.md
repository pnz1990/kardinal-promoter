# 17: Git Operations and GitHub PR Flow

> Status: Complete | Created: 2026-04-22
> See also: `pkg/scm/`, `pkg/update/`

---

## What this does

Implements the git clone → image update → commit → push → PR open flow that drives every environment promotion. Abstracts over SCM providers (GitHub first, GitLab planned).

---

## Present (✅)

- ✅ **`pkg/scm` package**: `SCMProvider` interface with `OpenPR()`, `MergePR()`, `ListPRs()`, `ClosePR()`, `GetPR()`. `GitHubProvider` implementation using `go-github`.
- ✅ **Zero-downtime credential rotation**: SCM provider re-reads the GitHub token secret on each reconcile call. No restart required.
- ✅ **`pkg/update` package**: `UpdateStrategy` interface. `kustomize-set-image` strategy (modifies `kustomization.yaml`). `helm-set-image` strategy (modifies `values.yaml`).
- ✅ **PR evidence body**: structured template with Bundle provenance, gate results, soak duration, CI run URL. `Closes #` linking to Bundle issue.
- ✅ **PR labels**: `kardinal/promote`, `kardinal/rollback` applied at PR creation. Configurable via Pipeline spec.
- ✅ **Git operations**: clone with token auth, create branch, commit, push, open PR — all idempotent (detect existing PR before creating).
- ✅ **Webhook reliability**: GitHub webhook events consumed via `pkg/scm/webhook.go`. Falls back to polling if webhook delivery fails.

---

## Future (🔲)

- 🔲 **GitLab support** (`pkg/scm/gitlab.go`) — Stage 17 work item.
- 🔲 **Bitbucket support** — not scheduled.

---

## Zone 1 — Obligations

**O1** — PR creation is idempotent: calling `OpenPR()` twice for the same Bundle+environment returns the existing PR, not a duplicate.
**O2** — Git operations retry on transient failures (rate limits, network errors) using exponential backoff.
**O3** — Token rotation takes effect within one reconcile loop (no stale token caching).
