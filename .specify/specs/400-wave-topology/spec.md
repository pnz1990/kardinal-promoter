# Spec: Wave Topology — `wave:` field on Pipeline environments (K-06)

> Feature ID: 400-wave-topology
> Issue: #450
> Milestone: v0.6.0 — Pipeline Expressiveness
> Status: Implemented
> PR: (see state.json)

## Background

Multi-region production rollouts are the most common real-world pipeline shape.
Users frequently need to deploy to many parallel environments (e.g., prod-eu,
prod-us, prod-ap) with ordered waves (wave 1 deploys, then wave 2 after all
wave 1 environments are Verified). Writing explicit `dependsOn` for every
environment pair is verbose and error-prone.

## User Stories

- As a platform engineer managing multi-region deployments, I want to assign
  environments to numbered waves so that wave N environments automatically
  depend on all wave N-1 environments without manually listing them.
- As a pipeline author, I want `wave:` and explicit `dependsOn` to be composable
  so I can mix automatic wave edges with specific manual dependencies.
- As an existing pipeline user, I want pipelines with no `wave:` fields to be
  unaffected (backward compatible).

## Functional Requirements

- **FR-001**: `EnvironmentSpec.Wave` (int, min 1, optional) assigns an environment
  to a numbered deployment wave.
- **FR-002**: Wave N environments automatically depend on all Wave N-1 environments.
  The translator generates these edges before the Graph is built.
- **FR-003**: Wave 1 environments have no wave-derived dependencies (they are the
  first wave group).
- **FR-004**: `wave:` and explicit `dependsOn` are composable. The final dependency
  set is the union of wave-derived edges and any explicit `dependsOn` entries,
  deduplicated.
- **FR-005**: Environments without a `wave:` field (Wave == 0) continue to use the
  sequential default (each depends on the previous in the list). This is the
  backward-compatible behavior.
- **FR-006**: A pipeline can mix wave environments and non-wave environments.
  Non-wave environments still follow sequential ordering.

## Non-Functional Requirements

- No new CRDs, no new reconcilers — wave topology is purely syntactic sugar in
  the Pipeline translator/graph builder.
- Implementation must not affect existing pipeline behavior (backward compat).

## Acceptance Criteria

### AC-001: 3-wave pipeline generates correct edges
Given a pipeline with staging (wave 1), prod-eu (wave 2), prod-us (wave 2), prod-ap (wave 3):
When the Graph is built,
Then prod-eu and prod-us both depend on staging, and prod-ap depends on both
prod-eu and prod-us.

### AC-002: Wave 1 environments have no wave-derived dependencies
Given environments with Wave == 1:
When the Graph is built,
Then those environments have no automatic dependencies derived from their wave number.

### AC-003: wave: and dependsOn union correctly
Given an environment with Wave == 2 and an explicit `dependsOn` that overlaps with
wave-derived dependencies:
When the Graph is built,
Then the dependency set is the union, deduplicated.

### AC-004: Non-wave pipelines are backward compatible
Given a pipeline with no wave fields:
When the Graph is built,
Then the sequential default ordering is unchanged.

### AC-005: Mixed wave/non-wave pipeline
Given a pipeline with some environments using wave: and others without:
When the Graph is built,
Then non-wave environments follow sequential ordering, wave environments follow
wave ordering.

## Out of Scope

- Runtime execution model changes — waves are purely a translator/graph concern.
- Wave validation beyond minimum=1 — ordering is user-defined.
- UI visualization of waves as groups — future enhancement.

## Design Notes

1. Implementation is in `pkg/graph/builder.go` as `expandWaveDeps()`, called
   before the main dependency-building loop. This matches Q3 (translator logic)
   per Graph-first architecture.
2. `api/v1alpha1/pipeline_types.go` has the `Wave int` field with
   `+kubebuilder:validation:Minimum=1` marker.
3. Tests are in `pkg/graph/builder_test.go` as `TestBuilder_WaveTopology_*` (4 tests).
4. User doc: `docs/pipeline-reference.md` §Wave Topology section.
5. Example: `examples/wave-topology/pipeline.yaml`.
