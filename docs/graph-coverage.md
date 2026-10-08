# Graph Coverage

kardinal aims to run every promotion decision on the
[kro Graph](https://kro.run/next/docs/concepts/graph/overview/). This page lists what already
runs on the Graph, what needs a workaround, and what still runs in kardinal's own
controllers. We move items onto the Graph over time, either by asking kro for the missing
feature or by contributing it ourselves.

> Last reviewed: 2026-09-29, against kro v0.10.0-rc.0 on a kind cluster. Upstream kro work
> was surveyed on 2026-10-02.
> The engineering record, with kro source references for every row, is the
> [Graph capability ledger](design/16-graph-capability-ledger.md).

---

## On the Graph today

| What | How the Graph does it |
|------|------------------------|
| Promotion order (`test` → `uat` → `prod`, fan-out, fan-in) | Each environment is a PromotionStep node. kro creates a node only after the nodes it depends on are Verified. |
| Gates block an environment | Each PolicyGate is a node. The environment's step is created only after every gate node for it is ready. |
| PR review tracking | Each environment gets a PRStatus node, which the step fills in when it opens a PR. The node has no `readyWhen`: the step node is ready only once the step is Verified, which for a step that opened a PR is after the merge. |
| Per-promotion metric analysis | A `perPromotion` MetricCheck read by an environment's gate becomes a MetricCheck node for that Bundle and environment, with the Bundle's version in its query. Its query only resolves once the upstream steps are Verified, so kro creates it then; its `spec.suspend` is `${!(bundle.status.phase in ["Available", "Promoting"])}`, so kro stops it when the Bundle finishes. The MetricCheck controller reads only its own spec. See [Metric Checks](metric-checks.md#per-promotion-analysis). |
| Watching application health | Read-only ref nodes watch the Deployment, Argo CD Application, Flux Kustomization, Rollout or Canary. |
| Pipeline changes mid-flight | The Graph is updated in place. Environments that are already Verified are not re-run, and a step that has started runs the step list it recorded, so an `approval` edit changes its steps from the next Bundle. The Bundle in flight still reaches `GraphReady` True, whether or not its step opened a PR. |
| Cleanup | Deleting a Bundle deletes its Graph, and kro deletes everything the Graph created. A step's open PR is closed first. In a namespace being deleted, the controller removes kro's finalizer once kro can no longer delete as the Graph ServiceAccount. |

## On the Graph, with a workaround

These run on the Graph, but kardinal has to bend the Graph to get the behavior it needs.

| What | Workaround today | What kro would need | Ledger |
|------|------------------|---------------------|--------|
| Wait until the previous environment is ready | A standalone Graph does not wait for readiness by default. kardinal wraps each dependency in a CEL expression that only resolves once the condition holds. | A Graph option to wait for dependencies to be ready. The engine already has it, but only for ResourceGraphDefinition instances. | [G1](design/16-graph-capability-ledger.md#g1-readywhen-does-not-gate-dependents-in-a-standalone-graph) |
| Stop a Superseded Bundle | kro cannot suspend a Graph, and deleting it deletes the Bundle's steps and gates, which are its history. Each step node only resolves while the Bundle is not Superseded, so the Graph creates no new step and keeps the ones it has. | A way to suspend a Graph. The suspend annotation applies only to ResourceGraphDefinition instances. | [G1](design/16-graph-capability-ledger.md#g1-readywhen-does-not-gate-dependents-in-a-standalone-graph) |
| Skip one environment for a Bundle | Using `includeWhen` to skip a node also skips every node after it. kardinal removes skipped environments when it builds the Graph instead, so a skip cannot depend on runtime state. | A way to skip a node without skipping its dependents. | [G2](design/16-graph-capability-ledger.md#g2-includewhen-is-contagious) |
| Health checks for tools that are not installed | One kind the cluster does not serve (for example Flux on an Argo CD cluster) fails the whole Graph. kardinal leaves those nodes out. | Nothing for the compile failure: kro's dynamic types (`${}` in `apiVersion`) already handle it. What is left is a way for a missing kind not to hold the Graph's `Ready`. kardinal plans to remove these nodes instead ([#1283](https://github.com/pnz1990/kardinal-promoter/issues/1283)). | [G4](design/16-graph-capability-ledger.md#g4-a-missing-crd-fails-the-whole-graph) |
| Permissions the Graph runs with | kro runs each Graph as a ServiceAccount. kardinal creates that ServiceAccount and its RoleBindings before it creates the Graph. | kro documents the identity model now, and by design will not create the identity. Left: docs for controllers that generate Graphs, and reporting a permission error as a named condition instead of failing the Graph. | [G5](design/16-graph-capability-ledger.md#g5-the-graph-identity-is-provisioned-outside-the-graph) |
| Knowing which Bundle owns an object | Objects the Graph creates have no owner reference, so kardinal labels them. In a namespace being deleted, kardinal removes kro's finalizer itself. | Safe teardown in a namespace being deleted. kardinal can set owner references to the Bundle in its own templates without any kro change. | [G7](design/16-graph-capability-ledger.md#g7-no-ownerreferences-on-graph-children) |

## Not on the Graph yet

These still run in kardinal's controllers because the Graph has no way to express them.

| What | Where it runs | Why it is not on the Graph | What kro would need | Ledger |
|------|---------------|----------------------------|---------------------|--------|
| Time: weekend and business-hour windows, soak timers | ScheduleClock and Bundle controllers | kro's CEL has no clock, and a Graph cannot re-check itself on a timer. | In review upstream: KREP-025 adds `time.now()` and re-checks a Graph when a time comparison flips, which covers soak timers and fixed windows. Weekly windows and time zones are left for a follow-up proposal. | [G8](design/16-graph-capability-ledger.md#g8-logic-still-outside-the-graph) |
| Gate rules (the CEL in `PolicyGate.spec.expression`) | PolicyGate controller | A Graph can read the data gate rules use, but not the clock, and it cannot record why a gate blocked, which `kardinal explain` shows. The controller writes `status.ready` and the reason, and the Graph waits on `status.ready`. | A clock (row above). The reason stays on the PolicyGate by design. | [G8](design/16-graph-capability-ledger.md#g8-logic-still-outside-the-graph) |
| Deciding "healthy, so Verified" | PromotionStep controller | A Graph cannot set a PromotionStep's status, and a node's `readyWhen` does not hold back the nodes after it. The controller makes the call, and the step node's `readyWhen` (`Verified`) carries it into the Graph. | Nothing: this stays in the controller by design. | [G3](design/16-graph-capability-ledger.md#g3-readywhen-may-only-reference-the-node-itself) |
| Checking health against the exact commit or images that were promoted | PromotionStep controller | Same as the row above. | Nothing: this stays in the controller by design. | [G3](design/16-graph-capability-ledger.md#g3-readywhen-may-only-reference-the-node-itself) |

A clock in CEL (KREP-025) would move the most logic onto the Graph: soak timers and time
windows. Health stays in the PromotionStep controller.

## Outside the Graph by design

- **Git and SCM work**: cloning, updating the image, pushing, opening the PR and detecting
  the merge. kro creates Kubernetes objects and does not call external systems. The
  PromotionStep object is the boundary: the Graph decides when a step exists, and the
  PromotionStep controller does the git work.
- **Building the Graph**: turning a Pipeline and a Bundle into a Graph spec, retiring older
  Bundles, and computing Bundle metrics.

## How items move onto the Graph

1. The gap is recorded in the [ledger](design/16-graph-capability-ledger.md) with evidence from
   the kro source and the kardinal workaround.
2. We open a [kro issue](https://github.com/kubernetes-sigs/kro/issues), or a PR when the fix is
   small, and link it from the ledger. kardinal issues that wait on it get the
   `blocked-on-upstream` label.
3. When a kro release ships the fix, we upgrade kro, delete the workaround, update this page
   and close the ledger entry.

## Upstream status

What kro has in flight for each gap, as of 2026-10-02. "Complete" means kardinal could delete
its workaround once it ships; "Partial" covers only part of the gap. Details and the full list
of items checked are in the [ledger](design/16-graph-capability-ledger.md#upstream-survey-2026-10-02).

| Gap | Upstream work | Coverage | Next step |
|-----|---------------|----------|-----------|
| G1: wait for ready dependencies | [KREP-006 propagation control](https://github.com/kubernetes-sigs/kro/pull/861), stalled since May | Partial | Ask kro for a per-Graph opt-in to the readiness gate it already has for ResourceGraphDefinitions |
| G1: stop a Superseded Bundle | None for Graphs (suspend exists only for ResourceGraphDefinition instances) | None | Ask kro to honor the suspend annotation on Graphs |
| G2: skip without skipping dependents | [KREP-018 partial dependencies](https://github.com/kubernetes-sigs/kro/pull/1125), inactive | Partial | None for now: kardinal's build-time skip covers the product |
| G3: health across nodes | None, and not needed | None | Close with [#1283](https://github.com/pnz1990/kardinal-promoter/issues/1283) |
| G4: optional nodes for missing CRDs | Dynamic types, already in kro | Complete for the compile failure | None: [#1283](https://github.com/pnz1990/kardinal-promoter/issues/1283) removes the nodes |
| G5: Graph identity | [kro#1402](https://github.com/kubernetes-sigs/kro/pull/1402) docs (merged); [kro#1465](https://github.com/kubernetes-sigs/kro/pull/1465) (open) | Partial | Ask kro to report permission errors as a named condition |
| G7: owner references and cleanup | [kro#1445](https://github.com/kubernetes-sigs/kro/pull/1445) deletion policy, for ResourceGraphDefinitions only | None | Ask kro for safe teardown in a namespace being deleted |
| G8: clock in CEL | [KREP-025](https://github.com/kubernetes-sigs/kro/pull/1376) (approved once, in review) and its draft [kro#1434](https://github.com/kubernetes-sigs/kro/pull/1434) | Partial | Feedback shared with the author; the Graph re-check is planned for the final version, weekly windows for a follow-up proposal |
| G8: gate rules, multi-cluster, custom functions | Nothing that applies to Graphs | None | No ask: time is the only real blocker |

No kro issue has been filed or commented on by kardinal yet.

## Closed

- **G6: Pipeline changes re-ran every environment.** This was a kardinal bug, not a kro gap.
  kro updates a Graph in place, and kardinal now does too. Fixed 2026-09-29.

Want to help? Pick a row, check the ledger entry for the proposed kro change, and open an
issue or discussion on [kardinal-promoter](https://github.com/pnz1990/kardinal-promoter/issues).
