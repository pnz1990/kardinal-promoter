# Spec: kardinal-agent binary (item 14.5)

## Design reference
- **Design doc**: `docs/design/07-distributed-architecture.md`
- **Section**: `§ Binaries — kardinal-agent (per shard)`
- **Implements**: 14.5 — kardinal-agent binary: separate `cmd/kardinal-agent/` entry point for
  spoke-cluster distributed mode; reads shard assignments, runs PromotionStep reconciler only.

---

## Zone 1 — Obligations

**O1 — `cmd/kardinal-agent/main.go` exists and compiles.**
`go build ./cmd/kardinal-agent/` must succeed with zero errors.

**O2 — The agent registers only the PromotionStep reconciler.**
The agent must NOT register: BundleReconciler, PipelineReconciler, PolicyGateReconciler,
MetricCheckReconciler, PRStatusReconciler, RollbackPolicyReconciler, ScheduleClockReconciler,
SubscriptionReconciler. Each of these is a control-plane concern.

**O3 — The `--shard` flag is required.**
If `--shard` is empty (or `KARDINAL_SHARD` env var unset), the agent must log a fatal error
and exit with code 1. An agent without a shard label would compete with the controller for
all PromotionSteps — this is invalid configuration.

**O4 — The PromotionStep reconciler receives the shard value.**
`psreconciler.Reconciler.Shard` must be set to the `--shard` flag value, which causes it
to filter to only PromotionSteps with matching `kardinal.io/shard` label.

**O5 — The agent does NOT embed the UI, webhook server, or bundle API.**
None of the HTTP servers from `cmd/kardinal-controller/` appear in `cmd/kardinal-agent/`.

**O6 — The agent does NOT import `web` package or `web/embed.go`.**
That package embeds the compiled React assets. The agent binary must stay small.

**O7 — `make build` includes the agent binary.**
Makefile must have a `build-agent` target and `build` must depend on it.
`BINARY_AGENT = bin/kardinal-agent` defined in Makefile.

**O8 — The design doc is updated.**
`docs/design/14-v060-roadmap.md` item 14.5 is moved from 🔲 Future to ✅ Present.
`docs/design/07-distributed-architecture.md` §Present section updated.

**O9 — Tests: at least one table-driven test for the agent's shard requirement.**
`cmd/kardinal-agent/main_test.go` must exist with a test that verifies the shard=empty
fatal-exit behavior (tested via integration or by testing the validation function, not
the binary startup itself — we test the pure validation function, not flag parsing).

---

## Zone 2 — Implementer's judgment

- The agent shares the scheme setup (kardinalv1alpha1 + clientgoscheme) with the controller.
- SCM credentials (--github-token) are still needed: PromotionStep reconciler opens PRs.
- Metrics and health probe ports default to different values than the controller
  (`:8085` and `:8086`) to allow co-location on the same node during testing.
- Leader election is optional (default off for agent — only one agent per shard).
- The agent logs `[kardinal-agent]` in startup messages to distinguish from controller logs.

---

## Zone 3 — Scoped out

- The agent does NOT implement shard auto-discovery or label watching.
  Shard assignment is via `--shard` flag or `KARDINAL_SHARD` env var (already in controller).
- Health adapter HTTP calls are NOT moved into the agent in this PR.
  The existing PromotionStepReconciler already handles them; the agent just re-uses the same code.
- Helm chart changes for a separate agent Deployment are scoped out.
  The Helm chart is a separate PR (tracked in docs/design/07).
- The agent Dockerfile is scoped out — tracked as a follow-up.
