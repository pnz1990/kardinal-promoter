# Tasks: kardinal override (403-kardinal-override)

## Task Groups

### API types
- [x] PolicyGateOverride struct in api/v1alpha1/policygate_types.go
- [x] Overrides []PolicyGateOverride field in PolicyGateSpec
- [x] zz_generated.deepcopy.go updated (DeepCopyInto for PolicyGateOverride)
- [x] CRD YAML regenerated (config/crd/bases/kardinal.io_policygates.yaml)

### CLI command (FR-001-008)
- [x] cmd/kardinal/cmd/override.go — newOverrideCmd()
- [x] --gate, --reason, --stage, --expires-in flags
- [x] Patch PolicyGate.spec.overrides via PATCH operation
- [x] root.go: root.AddCommand(newOverrideCmd())

### Tests (5 tests)
- [x] TestOverrideFn_BasicOverride
- [x] TestOverrideFn_InvalidExpiry
- [x] TestOverrideFn_GateNotFound
- [x] TestOverrideFn_MultipleOverrides
- [x] TestOverrideFn_EmptyStageAppliesGlobally

### Reconciler (FR-005-006)
- [x] findActiveOverride() in pkg/reconciler/policygate/reconciler.go
- [x] Override check before CEL evaluation in Reconcile()
- [x] Reconciler tests updated

### Docs
- [x] docs/cli-reference.md: kardinal override section
- [x] docs/policy-gates.md: Emergency Overrides (K-09) section

## Verify Tasks

All [x] items have real implementation. Zero phantom completions.

Evidence:
- cmd/kardinal/cmd/override.go: 164 lines
- cmd/kardinal/cmd/override_test.go: 160 lines, 5 tests passing
- pkg/reconciler/policygate/reconciler.go: findActiveOverride() + Reconcile() check
- go test ./cmd/kardinal/cmd/... -run Override: PASS
