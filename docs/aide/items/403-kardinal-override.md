# Item 403: K-09 — kardinal override with mandatory audit record

> Queue: queue-017
> Issue: #451
> Priority: high
> Size: s
> Milestone: v0.6.0 — Pipeline Expressiveness

## Summary

New `kardinal override` CLI command. Patches `PolicyGate.spec.overrides` with a time-limited record containing `{gate, stage, reason, expiresAt, at}`. The PolicyGate reconciler checks for a non-expired override before evaluating CEL — if override present, gate passes. Override record appears in Bundle status and PR evidence.

## Acceptance Criteria

- [ ] `kardinal override <pipeline> --stage <env> --gate <gate-name> --reason "<text>" --expires-in 1h`
- [ ] CLI creates a `PolicyGateOverride` entry in `PolicyGate.spec.overrides[]`
- [ ] PolicyGate reconciler: if a non-expired override exists for the current stage, set `status.ready=true` with `reason: "override: <reason>"`
- [ ] Override expires: after `expiresAt`, reconciler evaluates CEL normally (not re-patching spec — just ignores expired overrides)
- [ ] `Bundle.status.environments[].gateResults[]` shows override records
- [ ] PR evidence body includes override entries in the policy compliance table with "OVERRIDDEN" badge
- [ ] Unit tests: override present, override expired, override for different stage (ignored)
- [ ] `docs/policy-gates.md` updated with override usage and audit trail docs
- [ ] `docs/cli-reference.md` updated with `override` command

## Package

`api/v1alpha1/policygate_types.go` — add `PolicyGateOverride` type + `Overrides []PolicyGateOverride`
`pkg/reconciler/policygate/reconciler.go` — check override before CEL
`cmd/kardinal/cmd/override.go` — new CLI command
