# Graph Coverage

kardinal aims to run every promotion decision on the
[kro Graph](https://kro.run/next/docs/concepts/graph/overview/). This page lists what already
runs on the Graph, what needs a workaround, and what still runs in kardinal's own
controllers. We move items onto the Graph over time, either by asking kro for the missing
feature or by contributing it ourselves.

> Last reviewed: 2026-09-29, against kro v0.10.0-rc.0 on a kind cluster.
> The engineering record, with kro source references for every row, is the
> [Graph capability ledger](design/16-graph-capability-ledger.md).

---

## On the Graph today

| What | How the Graph does it |
|------|------------------------|
| Promotion order (`test` → `uat` → `prod`, fan-out, fan-in) | Each environment is a PromotionStep node. kro creates a node only after the nodes it depends on are Verified. |
| Gates block an environment | Each PolicyGate is a node. The environment's step is created only after every gate node for it is ready. |
| PR review tracking | Each environment gets a PRStatus node, which the step fills in when it opens a PR. The node has no `readyWhen`: the step node is ready only once the step is Verified, which for a step that opened a PR is after the merge. |
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
| Health checks for tools that are not installed | One kind the cluster does not serve (for example Flux on an Argo CD cluster) fails the whole Graph. kardinal leaves those nodes out. | Optional nodes that stay pending instead of failing the Graph. | [G4](design/16-graph-capability-ledger.md#g4-a-missing-crd-fails-the-whole-graph) |
| Permissions the Graph runs with | kro runs each Graph as a ServiceAccount. kardinal creates that ServiceAccount and its RoleBindings before it creates the Graph. | A documented or built-in way to provision a Graph's identity. | [G5](design/16-graph-capability-ledger.md#g5-the-graph-identity-is-provisioned-outside-the-graph) |
| Knowing which Bundle owns an object | Objects the Graph creates have no owner reference, so kardinal labels them. | An option to set owner references on created objects. | [G7](design/16-graph-capability-ledger.md#g7-no-ownerreferences-on-graph-children) |

## Not on the Graph yet

These still run in kardinal's controllers because the Graph has no way to express them.

| What | Where it runs | Why it is not on the Graph | What kro would need | Ledger |
|------|---------------|----------------------------|---------------------|--------|
| Time: weekend and business-hour windows, soak timers | ScheduleClock and Bundle controllers | kro's CEL has no clock, and a Graph cannot re-check itself on a timer. | A `now` value in CEL plus a periodic re-evaluation, or a built-in clock node. | [G8](design/16-graph-capability-ledger.md#g8-logic-still-outside-the-graph) |
| Gate rules (the CEL in `PolicyGate.spec.expression`) | PolicyGate controller | Gate rules read the clock, metrics and upstream soak time, which kro's CEL cannot see. The controller writes `status.ready`, and the Graph waits on that. | Custom CEL functions or data sources in the Graph. | [G8](design/16-graph-capability-ledger.md#g8-logic-still-outside-the-graph) |
| Deciding "healthy, so Verified" | PromotionStep controller | A node's `readyWhen` can only look at the node itself, so a step cannot say "I am done when the Deployment is healthy". The health ref nodes are shown on the Graph, but the controller makes the call. | `readyWhen` that can read the nodes a node depends on. | [G3](design/16-graph-capability-ledger.md#g3-readywhen-may-only-reference-the-node-itself) |
| Checking health against the exact commit or images that were promoted | PromotionStep controller | Needs the same cross-node `readyWhen` as the row above. | Same as above. | [G3](design/16-graph-capability-ledger.md#g3-readywhen-may-only-reference-the-node-itself) |

The G3 fix and a clock in CEL would move the most logic onto the Graph: health, soak timers
and time windows.

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

| Gap | kro issue or PR |
|-----|-----------------|
| G1: wait for ready dependencies | Not filed yet |
| G2: skip without skipping dependents | Not filed yet |
| G3: `readyWhen` across nodes | Not filed yet |
| G4: optional nodes for missing CRDs | Not filed yet |
| G5: Graph identity | Not filed yet |
| G7: owner references on created objects | Not filed yet |
| G8: clock and custom functions in CEL | Not filed yet |

## Closed

- **G6: Pipeline changes re-ran every environment.** This was a kardinal bug, not a kro gap.
  kro updates a Graph in place, and kardinal now does too. Fixed 2026-09-29.

Want to help? Pick a row, check the ledger entry for the proposed kro change, and open an
issue or discussion on [kardinal-promoter](https://github.com/pnz1990/kardinal-promoter/issues).
