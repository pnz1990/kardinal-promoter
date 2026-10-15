# Freeze agents, stop the line

A freeze holds promotions until it is lifted, in every Graph, including versions already on
their way. An **agent freeze** holds the versions agents created and lets people's through. A
**stop-the-line** holds every version. The controller enforces it, not the agent: an agent
cannot ignore a freeze.

```bash
# Freeze agents in every namespace until lifted
kardinal freeze --agents --reason "INC-4211: agents paused while we investigate"

# Stop the line for everyone in one namespace, for two hours
kardinal freeze --everyone -n payments --reason "payments incident" --for 2h

# List the freezes
kardinal freeze

# Lift one
kardinal unfreeze kardinal-freeze-agents
```

## What a freeze holds

A held step waits before it starts, or before its next git step, and its message names the
freeze, who froze, since when and why, and the two ways on. A step that is waiting for its PR
merge or running its health check finishes, as with [pause](../pause.md): stopping it would leave
a merged change unverified. Auto-merge of a held step's PR is off.

| Freeze | Holds |
|---|---|
| `--agents` | Versions whose actor stamp says kind `agent` ([Actors](../actors.md)) |
| `--everyone` | Every version |

`--cluster` (the default) covers every namespace; `-n <namespace>` covers one. `--for` ends the
freeze by itself; without it, the freeze lasts until `kardinal unfreeze`. Freezing the same scope
again updates its reason and end.

A freeze does not refuse what agents write: an agent can still create versions, which then wait.
To refuse every write of one agent, suspend its AgentIdentity.

## Let one change through

During a freeze, `kardinal override --freeze` lets a pipeline through every freeze that holds
its namespace, for one environment, one version, or both, for a while (default one hour):

```bash
kardinal override checkout prod --freeze --reason "hotfix for INC-4211" --expires-in 30m
```

The exemption is recorded on the freeze with your name and your actor stamp, and when a step goes
on under it, the step writes a `FreezeExempted` AuditEvent for each freeze it goes through, naming
who let it through and why. Only someone who may edit the freeze can add an exemption: a
namespace's gate overrides cannot break a cluster freeze.

An exemption counts for at most a day from when the controller first sees it, whatever
`--expires-in` says, as gate overrides are capped (Helm `controller.freezeExemptionMaxMinutes`,
`--freeze-exemption-max-minutes`; `status.exemptionRecords` shows each exemption's end).
Break-glass is for people: the `<release>-actor` admission policy refuses an exemption from an
agent, and the controller never honours one stamped as an agent's (the freeze's
`ExemptionIgnored` condition says so).

## In the UI

Every view shows a banner while a freeze is active, with **Unfreeze** for those who may lift it.
The top bar's **Freeze** menu offers **Freeze agents…** and **Stop the line…** (which asks you to
type `stop`). The controls need the UI's TokenReview sign-in (a freeze names who froze) and Helm
`ui.freezeControls: true`, which lets the controller write freezes for allowed callers.

## Who may freeze

A freeze is a cluster-scoped ChangeWindow, so freezing takes cluster rights. The chart's
`<release>-freezer` ClusterRole grants them (and the UI's `changewindows/freeze` action): bind it
to your incident commanders.

In the UI, the `changewindows/freeze` action in one namespace (the chart's admin role grants it
through a RoleBinding) lets a team freeze its own namespace, and change or lift only the freezes it
made there. Lifting or changing someone else's freeze of the namespace, such as an incident
commander's stop-the-line, takes the action cluster-wide: the UI answers 403.

```bash
kubectl create clusterrolebinding incident-freezers \
  --clusterrole kardinal-promoter-freezer --group incident-commanders
```

The `<release>-freezes` admission policy records who froze (`spec.freeze.createdBy`) and who made
each exemption, as the API server authenticated them; nobody can name someone else.

## Under the hood

`kardinal freeze` writes a ChangeWindow named `kardinal-freeze-<agents|everyone>[-<namespace>]`:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: ChangeWindow
metadata:
  name: kardinal-freeze-agents
spec:
  type: blackout
  start: "2026-10-14T09:00:00Z"   # no end: until it is removed
  reason: "INC-4211: agents paused while we investigate"
  freeze:
    actors: agents                 # agents | everyone
    namespaces: []                 # exact names or "prefix*"; empty: every namespace
    createdBy: alice@example.com
    exemptions: []
```

The ChangeWindow reconciler writes whether it is active and the exemptions in effect to its
status at each boundary; the PromotionStep reconciler holds steps on that status. A freeze can
also be named in a gate expression like any ChangeWindow (`changewindow.isBlocked(...)`).
The design: [18-actor-policy §9](../design/18-actor-policy.md#9-agent-freeze-and-stop-the-line).
