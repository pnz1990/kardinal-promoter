# Item 400: K-06 — wave: field on stages as DAG shorthand

> Queue: queue-017
> Issue: #450
> Priority: high
> Size: m
> Milestone: v0.6.0 — Pipeline Expressiveness

## Summary

Add `wave:` integer field to `EnvironmentSpec`. The translator generates automatic `dependsOn` edges: wave N stages depend on all wave N-1 stages. Pure translator change — no new CRD, no new reconciler.

## Acceptance Criteria

- [ ] `Pipeline.spec.environments[].wave` field (int, optional, validated ≥ 1)
- [ ] Translator generates edges: every wave-N stage depends on every wave-(N-1) stage
- [ ] `wave:` and explicit `dependsOn:` are composable (union of edges)
- [ ] Unit tests cover: 3-wave pipeline, 2-wave + serial, mixed wave + explicit dependsOn
- [ ] `examples/wave-topology/pipeline.yaml` works with 3 waves
- [ ] `docs/pipeline-reference.md` documents the `wave:` field
- [ ] `go test ./pkg/translator/... -race` passes with new tests

## Package

`api/v1alpha1/pipeline_types.go` — add `Wave int` field
`pkg/translator/translator.go` — extend edge generation
