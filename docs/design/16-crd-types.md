# 16: CRD Types and Validation

> Status: Complete | Created: 2026-04-22

---

## What this does

Defines the Kubernetes Custom Resource Definitions (CRDs) for Pipeline, Bundle, PromotionStep, PolicyGate, and supporting types. All kardinal state lives in these CRDs — no external database.

---

## Present (✅)

- ✅ **`Pipeline` CRD**: `spec.environments[]` with name, repoURL, updateStrategy. `spec.policyGates[]` linking gate names to environment order. `spec.scm` config. Validated by CEL rules.
- ✅ **`Bundle` CRD**: `spec.image`, `spec.pipeline`, `spec.provenance` (author, commitSHA, ciRunURL). `status.phase` (Pending/Promoting/Verified/Failed/Superseded).
- ✅ **`PromotionStep` CRD**: per-environment promotion record. `spec.bundleRef`, `spec.environment`. `status.phase` state machine.
- ✅ **`PolicyGate` CRD**: `spec.expression` (CEL), `spec.type` (pre/post-deploy). `status.ready`, `status.lastEvaluatedAt`, `status.reason`.
- ✅ **`MetricCheck` CRD**: `spec.query`, `spec.passThreshold`. `status.value`, `status.result` (Pass/Fail).
- ✅ **`ChangeWindow` CRD**: `spec.schedule` (cron), `spec.timezone`. CEL `changewindow.<name>` variable.
- ✅ **CRD validation**: CEL validation rules on all CRDs. `Subscription` CRD for multi-pipeline fan-in.
- ✅ **Conversion webhooks**: not required — CRDs are v1 only, no storage version migration.

---

## Future (🔲)

- 🔲 **CRD versioning (v1alpha1 → v1beta1 → v1)**: when API stabilizes, add conversion webhooks and storage version migration. Not scheduled.

---

## Zone 1 — Obligations

**O1** — All CRDs registered with the controller-runtime scheme at startup.
**O2** — `Bundle.status.phase` follows the state machine: Pending → Promoting → (Verified | Failed | Superseded).
**O3** — All CRD fields validated; malformed resources are rejected at admission.
