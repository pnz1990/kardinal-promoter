# Freeze agents, stop the line

!!! info "New in v0.12.0"
    v0.11.0 and earlier do not have this. The [changelog](../changelog.md) lists every v0.12.0 change.

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

The step records the freeze in `status.frozenBy` (its name and since when) and clears it when no
freeze holds it any more, or when a [pause](../pause.md) holds it instead. The Pipeline's `status.environments[].frozenBy`
names the freeze for each environment it holds, and `kardinal get pipelines` and
`kardinal status` show such an environment as "Frozen by <freeze>".

| Freeze | Holds |
|---|---|
| `--agents` | Versions whose actor stamp says kind `agent`, or `unverified` (written while the agent index wasn't Ready: [Actors](../actors.md#while-the-index-isnt-ready)) |
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
agent, and the controller never honours one stamped as an agent's, or as `unverified` (written
while the agent index wasn't Ready: break-glass then waits for the index,
[Actors](../actors.md#while-the-index-isnt-ready)). The freeze's `ExemptionIgnored` condition
says which and why.

## Who froze, and who lifted it

A freeze's start and its lift are AuditEvents, `FreezeStarted` and `FreezeLifted`, naming who
and their verified actor: in the freeze's namespace, or the controller's (the watched namespace
in namespace mode) for a freeze of several or every namespace, or when the frozen namespace is
gone or the controller may not write there. `kardinal unfreeze` and the UI record who lifts it
(`spec.freeze.lifted`) just before they delete the window, and a finalizer
(`kardinal.io/freeze-audit`) keeps the window until `FreezeLifted` is written, so a
`kubectl delete` is recorded too, without an actor. While the controller is down, a deleted freeze
waits for it. A freeze being deleted (by `kardinal unfreeze`, the UI or `kubectl delete`, and
kept by the finalizer) is gone for everything else at once: it holds no step, takes no
`kardinal override --freeze` exemption, is not listed by `kardinal freeze`, the UI or the MCP
`freezes` tool, and is false in a gate's `changewindow.<name>`. Freezing its scope again
meanwhile is refused with `freeze <name> is being lifted; try again shortly (up to 2 minutes; the controller must be running)`.
`spec.freeze.lifted` only names who lifts it: when the delete fails, `kardinal unfreeze` reports
the error and takes the mark back, and the freeze stays in force. A freeze frozen again while an
unfreeze is under way is not deleted: the unfreeze says it changed, and you run it again if you
still mean to lift it. A second unfreeze of a freeze already being deleted changes nothing, so the
record names who lifted it first.

An audit write never holds a freeze back. A freeze takes effect (its status is written) whether
or not its `FreezeStarted` record could be written; a failed write is retried, with a
`FreezeAuditFailed` Warning Event. A deleted freeze whose `FreezeLifted` record still cannot be
written after 2 minutes is let go without it, with the same Event.

## In the UI

Every view shows a banner for each active freeze: whom it holds and where, who froze (a person
or an automation, as in [Who shipped a version](../ui.md#who-shipped-a-version)), since when and
until when, the reason, and how many exemptions are in effect. Stopping the line is in the failed
colour, an agent freeze in the blocked one, and the words say which. **Unfreeze** is there for
those who may lift it; for everyone else, and while the controls below are off, the banner shows
`kardinal unfreeze <name>`, with the controller's reason as its tooltip. The stage drawer of an
environment a freeze holds says so in **Why it's waiting**, with the step's message: the freeze,
and the commands to lift it or let this environment through. On the station map and the fleet
board, a station a freeze holds is marked frozen (❄, "Frozen by <freeze>"), and **Needs
attention** counts it.
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
