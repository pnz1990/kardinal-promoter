# Tasks: Wave Topology (400-wave-topology)

## Task Groups

### FR-001/002/003/004/005/006: Core Implementation

- [x] Add `Wave int` field to `api/v1alpha1/pipeline_types.go` with
      `+kubebuilder:validation:Minimum=1` and descriptive comment
- [x] Implement `expandWaveDeps()` in `pkg/graph/builder.go` to build
      wave-derived dependency edges for environments with Wave > 0
- [x] Wire `expandWaveDeps()` into the main dependency-building loop in
      `pkg/graph/builder.go` so wave edges are applied before sequential
      default and explicit dependsOn
- [x] Ensure wave edges and explicit dependsOn are unioned and deduplicated

### Tests (TDD)

- [x] `TestBuilder_WaveTopology_3Waves` — 3-wave pipeline generates correct edges
- [x] `TestBuilder_WaveTopology_2Wave_Plus_Serial` — mixed wave + sequential default
- [x] `TestBuilder_WaveTopology_WithExplicitDependsOn` — wave + dependsOn union
- [x] `TestBuilder_WaveTopology_NoWave_BackwardCompat` — no wave = unchanged behavior

### Docs

- [x] `docs/pipeline-reference.md` — `wave:` field in EnvironmentSpec table
- [x] `docs/pipeline-reference.md` — §Wave Topology section with YAML example
- [x] `examples/wave-topology/pipeline.yaml` — complete 5-env multi-region example
- [x] `examples/wave-topology/README.md` — usage notes

## Verify Tasks

All [x] items have real implementation. Zero phantom completions.

Evidence:
- `pkg/graph/builder.go`: `expandWaveDeps()` implemented at line 826
- `api/v1alpha1/pipeline_types.go`: `Wave int` field at line 132
- `pkg/graph/builder_test.go`: 4 WaveTopology tests pass
- `go test ./pkg/graph/... -run WaveTopology`: PASS
