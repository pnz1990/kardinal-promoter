# Spec: ValidatingAdmissionWebhook for dependsOn cycle detection

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future — Lens 4: Security posture`
- **Implements**: "No admission webhook for dependsOn cycle detection" (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable; QA must verify each)

**O1**: A `Pipeline` object with a circular `dependsOn` (e.g. `prod → uat`, `uat → prod`) MUST be
rejected with HTTP 400 and a descriptive error message at `kubectl apply` time when the
admission webhook is registered and the controller is running with `--pipeline-admission-webhook=true`.

**O2**: The webhook handler MUST be mounted at `POST /webhook/validate/pipeline` on the existing
webhook server (`:8083`). No new server port is introduced.

**O3**: The webhook handler MUST return a valid `admissionv1.AdmissionReview` JSON response with
`response.allowed=false` and a non-empty `response.status.message` containing the cycle path
when a cycle is detected.

**O4**: A `Pipeline` object with no circular dependency MUST be admitted (allowed=true) by the
webhook.

**O5**: When the translator encounters a cycle error (the admission webhook is disabled or bypassed),
the Bundle reconciler MUST set `status.conditions` with type=`InvalidSpec`, reason=`CircularDependency`
AND status=`False/Failed` on the Bundle — not just `TranslationError`. This makes cycle errors
distinguishable from other translation failures in the Bundle status.

**O6**: The `--pipeline-admission-webhook` flag (default: `false`) enables the handler registration.
When `false`, `/webhook/validate/pipeline` returns 404. The flag is readable from
`KARDINAL_PIPELINE_ADMISSION_WEBHOOK=true` environment variable.

**O7**: The cycle detection logic MUST be a pure function in `pkg/graph/` that takes a `*Pipeline`
and returns an error — no reconciler calls, no external I/O.

**O8**: At least 3 unit tests in `pkg/graph/cycle_test.go` cover: (a) no cycle (linear chain),
(b) direct 2-node cycle (A→B, B→A), (c) indirect 3-node cycle (A→B→C→A).

**O9**: At least 2 unit tests in `pkg/admission/pipeline_webhook_test.go` cover: (a) admitted when
no cycle, (b) rejected when cycle present.

---

## Zone 2 — Implementer's judgment

- The `ValidatingWebhookConfiguration` Kubernetes resource is NOT created by this PR —
  operators must create it themselves pointing to `/webhook/validate/pipeline` on the
  controller service. This avoids coupling the controller to cluster-admin webhook registration.
  The Helm chart can include a commented example.
- The cycle function can reuse the existing `resolveOrdering` logic in `pkg/graph/builder.go` —
  extract or call it directly. Do not duplicate the Kahn's algorithm.
- `AdmissionReview` parsing: use `encoding/json` + the standard
  `k8s.io/api/admission/v1` types already in go.mod.

---

## Zone 3 — Scoped out

- Automatic `ValidatingWebhookConfiguration` creation (requires cluster-admin RBAC at install time)
- Cycle detection for PolicyGate or Bundle CRDs (not applicable — they have no dependsOn)
- Testing against a real Kubernetes API server (unit tests with fake AdmissionReview are sufficient)
