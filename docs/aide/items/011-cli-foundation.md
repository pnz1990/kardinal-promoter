# Item 011: CLI Foundation — cobra setup + core commands (Stage 8 partial)

> **Queue**: queue-005
> **Branch**: `011-cli-foundation`
> **Depends on**: 005 (merged — CRD types), 006 (merged — BundleReconciler)
> **Dependency mode**: merged
> **Assignable**: immediately (Stage 2 is done)
> **Contributes to**: J5 (CLI workflow)
> **Priority**: MEDIUM — J5 requires full CLI (Stage 8)

---

## Goal

Implement the `kardinal` CLI binary foundation with cobra and the first set of
read-only commands: `version`, `get pipelines`, `get bundles`, `get steps`.

Design spec: `docs/aide/roadmap.md Stage 8` and `docs/cli-reference.md`

---

## Deliverables

### 1. CLI entry point: `cmd/kardinal/main.go`

Cobra root command with persistent flags:
- `--namespace` / `-n`: Kubernetes namespace (default: current context namespace)
- `--kubeconfig`: path to kubeconfig (default: `$KUBECONFIG` or `~/.kube/config`)
- `--context`: kubeconfig context override

### 2. `kardinal version`

```
CLI: v0.1.0-dev
Controller: <reads from ConfigMap kardinal-system/kardinal-version, or "unknown" if not found>
```

### 3. `kardinal get pipelines [name]`

Table output:
```
PIPELINE    PHASE   ENVIRONMENTS   PAUSED   AGE
nginx-demo  Ready   3              false    2m
```

### 4. `kardinal get bundles [pipeline]`

Table output:
```
BUNDLE              TYPE    PHASE      AGE
nginx-demo-v1-29-0  image   Promoting  45s
```

### 5. `kardinal get steps <pipeline>`

Table output matching `kardinal explain`:
```
ENVIRONMENT   STEP-TYPE              STATE    MESSAGE
test          kustomize-set-image    Pending  -
uat           kustomize-set-image    Pending  -
prod          PolicyGate             Pending  no-weekend-deploys
```

Lists all PromotionStep CRDs for the given pipeline (by `kardinal.io/pipeline` label).

### 6. Unit tests

Table-driven tests for output formatting functions. Command tests using cobra test helpers.

---

## Acceptance Criteria

- [ ] `kardinal version` prints CLI and controller versions
- [ ] `kardinal get pipelines` shows table with correct columns
- [ ] `kardinal get bundles` shows table with correct columns
- [ ] `kardinal get steps` shows PromotionStep states
- [ ] All commands use `--namespace` flag (default: current context)
- [ ] `go build ./cmd/kardinal/...` produces a binary
- [ ] `go test ./cmd/kardinal/...` passes
- [ ] `go vet ./...` passes
- [ ] Copyright headers on all new files
- [ ] No banned filenames

---

## Notes

- Use `k8s.io/client-go` with controller-runtime client (already in go.mod)
- Table output: use `text/tabwriter` from stdlib
- Version: read from embedded build info via `runtime/debug.ReadBuildInfo()`
- Do NOT add new external CLI dependencies (cobra is already in go.mod)
