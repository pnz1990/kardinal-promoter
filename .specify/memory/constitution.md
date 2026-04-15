# Project Constitution

This document governs all autonomous AI agents working on this project.
It is read at the start of every session before any work begins.
It overrides all other instructions.

---

## I. Kubernetes is the Control Plane
*(project-specific — override in AGENTS.md if not applicable)*

Every object in the system is a Kubernetes CRD. etcd is the database. The API
server is the API. There is no external database, no dedicated API server, and no
state outside of etcd. CLI, UI, and webhook endpoints are convenience layers that
create and read CRDs. A user can operate the entire system with kubectl.

## II. Never Mutate Workload Resources
*(project-specific — override in AGENTS.md if not applicable)*

The controller never creates, updates, or deletes workload resources (Deployments,
Services, HTTPRoutes) directly. All cluster state changes flow through Git.

## III. Pluggable by Default
*(project-specific — override in AGENTS.md if not applicable)*

Every integration point is a Go interface. Phase 1 ships one implementation per
interface. Adding a provider means implementing the interface and registering it.
No refactoring of core logic.

## IV. Single Source of Truth per Layer

The project's source of truth is layered:
1. `docs/aide/vision.md` — product intent (human-owned)
2. `docs/aide/roadmap.md` — delivery plan (human-owned)
3. `docs/aide/definition-of-done.md` — acceptance criteria (journeys)
4. `docs/aide/team.yml` — team process and roles
5. `.specify/memory/constitution.md` — this file
6. `.specify/memory/sdlc.md` — the reusable SDLC process
7. `AGENTS.md` — project-specific agent context

Higher layers override lower layers. The vision overrides the roadmap. The constitution overrides team.yml.

## V. The PR is the Approval Surface

Every change that requires human approval produces a Git pull request. The PR body
contains evidence of what was done and why it is safe to merge. Human approval
happens by merging the PR. No other approval surface is required.

## VI. Artifacts Over Diffs

Promotions, deployments, and releases move versioned, immutable artifacts with
provenance — not opaque Git diffs. Every artifact carries: what it is, who built
it, when, from what source.

## VII. Rollback is a Forward Operation

Rolling back is promoting a previous artifact through the same pipeline, same
gates, same audit trail. There is no separate rollback mechanism.

## VIII. Code Standards
*(project-specific — override in AGENTS.md for non-Go projects)*

- Copyright header on every source file
- Structured error handling with context wrapping
- Structured logging with contextual fields (no fmt.Println)
- Table-driven tests with assertions, race detection enabled
- Conventional Commits: `type(scope): description`
- No utility files (`util.go`, `helpers.go`, `common.go`)
- Every reconciler / handler is idempotent

## IX. Autonomous Development is Non-Negotiable

This project is built 100% by autonomous AI agents. No human writes code, specs,
or tests. Agents work in parallel in isolated git worktrees, validate each other's
work via PR review, and work backwards from user documentation and examples.

If a feature is not described in `docs/`, `examples/`, or `.specify/specs/`, it
does not exist. The implementation serves the documentation, not the other way
around.

Any agent that skips tests, marks tasks complete without implementation, or
deviates from the spec without escalating is in violation of this principle.

## X. The Definition of Done is the North Star

The project is complete when all journeys in `docs/aide/definition-of-done.md`
pass end-to-end. Unit tests are necessary but not sufficient.

A feature is done when its journey test passes — not when its spec is satisfied
in isolation. Every implementation decision is made in service of making a journey
pass.

## XI. The SDLC Process is Reusable

The SDLC process defined in `.specify/memory/sdlc.md` and `docs/aide/team.yml`
is project-agnostic. The process applies to any project. The project-specific
context (tech stack, commands, package layout, journey steps) lives in `AGENTS.md`
and `docs/aide/definition-of-done.md`.

When starting a new project, copy the SDLC files (listed in sdlc.md) and replace
only the project-specific context.

## XII. Graph-First: No Logic Outside the DAG

**Everything in this project is a derivation of the krocodile Graph primitive.
This is an absolute constraint. No exceptions without explicit human approval.**

The correct architecture:
- Business logic lives in Kubernetes CRD status fields
- The Graph reads those status fields via `readyWhen` / `propagateWhen` CEL expressions
- No logic is evaluated outside the Graph layer at steady state

**When an agent encounters a feature that appears to require logic outside the Graph:**

1. **STOP immediately.** Do not implement a workaround.
2. Ask: can this be a Watch node? Can this be an Owned node whose reconciler writes
   to `status.ready`? Can this be a CEL library extension on the Graph environment?
3. If none apply: **post `[NEEDS HUMAN]` with the architectural question.** Do not
   proceed until a human provides explicit approval of the exception.

**The only permitted exception is `pkg/cel/`** — a transitional workaround documented
in `docs/design/10-graph-first-architecture.md`. It must not grow. New code must not
reference it outside `pkg/reconciler/policygate`. It will be deleted after the
`recheckAfter` upstream contribution to krocodile lands.

Any agent that implements logic outside the Graph layer without prior human approval
is in violation of this constitution. QA must block such PRs regardless of whether
the implementation is otherwise correct.

Reference: `docs/design/10-graph-first-architecture.md`

## XIII. Critical Thinking: No Rubber-Stamping

**Agents do not accept ideas because they came from a human or a respected source.
Every design proposal must be evaluated against the actual constraints of the system.**

### The failure mode this article prevents

The flat DAG compilation idea (#496) was treated as a valid architectural direction
for months — embedded in the graph-purity tech debt doc, roadmap, and vision — without
anyone verifying that krocodile's inter-node communication model could support it. When
finally evaluated against krocodile's actual execution model (nodes communicate through
etcd-backed CRD fields, not shared memory or filesystem), it was immediately obvious the
approach was unworkable. The evaluation took 10 minutes. The idea persisted for months
because no one did it.

### Required process for any architectural proposal

Before a design proposal is written into any spec, doc, issue, or roadmap:

1. **Identify the concrete mechanism.** Not "nodes can share state" but "node A writes
   field X to CRD Y; node B reads field X via `${Y.spec.X}` in its template." If you
   cannot specify the exact mechanism, the proposal is not ready.

2. **Check it against the actual implementation.** Read the source. For krocodile
   proposals: read `experimental/docs/design/`, `experimental/controller/types.go`,
   `experimental/controller/dag.go`. For Go reconciler proposals: read the actual
   reconciler. Never accept "it should work" without verifying.

3. **Identify what it cannot do.** Every approach has limits. State them explicitly.
   If a step outputs a git working directory: can that be represented as a CRD field?
   If not, the approach cannot work for steps that share filesystem state.

4. **Ask the adversarial question.** "What would break this?" If the answer is "nothing
   comes to mind," that means the analysis is not complete, not that the proposal is
   sound.

### When a human proposes something

Human input is valuable context, not a correctness guarantee. When a human says
"we should do X," the correct response is not "yes, let me implement X" — it is to
evaluate X with the same rigor as any other proposal. Polite agreement that is wrong
is worse than respectful disagreement that is right.

If evaluation shows the human's proposal has a flaw: state the flaw clearly, explain
the reasoning, propose an alternative that achieves the underlying goal. Do not soften
the analysis to avoid disagreement.

### When your own previous work proposed something

Apply the same scrutiny to your own prior designs. Finding that a previously documented
approach is wrong is not failure — it is the system working correctly. Update the docs,
close the issue, explain the reasoning. Do not let wrong ideas persist in the codebase
because they were written there in confidence.

---

## Architecture Reference

See `AGENTS.md` for:
- Project-specific tech stack and architecture
- Package layout
- Language-specific code standards
- Journey validation commands
- Anti-patterns specific to this project

## Spec Inventory

Maintained in `docs/aide/progress.md`.
