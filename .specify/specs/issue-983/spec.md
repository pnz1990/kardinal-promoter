# Spec: Namespace-scoped controller mode

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future` — Lens 6: New gaps (competitive)
- **Implements**: No namespace-scoped controller mode (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

### O1 — `--watch-namespace` flag on the controller binary
`cmd/kardinal-controller/main.go` MUST accept a `--watch-namespace` flag (env: `KARDINAL_WATCH_NAMESPACE`).
- Default: `""` (cluster-wide mode, current behavior unchanged)
- When set: the controller-runtime manager MUST be created with `Cache: cache.Options{DefaultNamespaces: {ns: {}}}` so the informer cache is scoped to the given namespace only.
- Violation: controller watching resources outside the specified namespace when `--watch-namespace` is set.

### O2 — Helm `controller.watchNamespace` value
`chart/kardinal-promoter/values.yaml` MUST add `controller.watchNamespace: ""` (default empty = cluster-wide).
When non-empty, the Helm deployment template MUST pass `KARDINAL_WATCH_NAMESPACE` as an env var to the controller pod.
- Violation: `controller.watchNamespace: "my-ns"` not propagating to the controller binary.

### O3 — Namespaced RBAC when watchNamespace is set
When `controller.watchNamespace` is non-empty:
- The Helm chart MUST render a `Role` (not `ClusterRole`) scoped to the watch namespace.
- The Helm chart MUST render a `RoleBinding` (not `ClusterRoleBinding`) in the watch namespace.
- The chart MUST NOT render the `ClusterRole` / `ClusterRoleBinding` when namespace-scoped mode is active.
- Violation: `ClusterRole` rendered when `controller.watchNamespace` is set.

### O4 — Backward compatibility: cluster-wide is the default
When `controller.watchNamespace` is empty (default), behavior is identical to the current release:
- `ClusterRole` + `ClusterRoleBinding` rendered (no change).
- No `Role` / `RoleBinding` rendered.
- No `KARDINAL_WATCH_NAMESPACE` env var injected.
- Violation: existing Helm installs with no `watchNamespace` getting broken by this change.

### O5 — Tests
`TestWatchNamespaceFlagParsed` MUST pass: verifies that when `KARDINAL_WATCH_NAMESPACE` is set, the controller options include the namespace in cache.DefaultNamespaces.
Helm template test: `TestHelmNamespacedRBAC` MUST verify that with `watchNamespace=foo`, the chart renders a Role (not ClusterRole) in namespace `foo`.
- Violation: any of these tests absent or failing.

---

## Zone 2 — Implementer's judgment

- Leader election lock namespace: keep as controller-runtime default (Release.Namespace) regardless of watchNamespace.
- Whether to support comma-separated multiple namespaces — single namespace only for MVP.
- Whether to add a startup log line noting namespace scope — add it for observability.

---

## Zone 3 — Scoped out

- Multi-namespace watch (watching a list of namespaces) — single namespace for MVP.
- Per-namespace RBAC for the kro Graph CRDs — the Graphs are owned by the controller and follow the controller's install namespace.
- Documented workaround for multi-tenant without namespace-scoped mode — out of scope for this PR.
