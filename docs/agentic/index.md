# Agentic delivery

**GitOps Governance for the Agentic Era.**

Coding agents now write and ship a growing share of changes, and a guardrail that lives inside
the agent is one the agent can get around. kardinal enforces governance where the change lands:
in the cluster, not in the agent. Every promotion kardinal makes passes the same checks, the
Kubernetes API server's admission policies and the controller's gates, whoever started it: a
person, a CI system or an agent.

## What kardinal enforces today

Everything below is shipped, and applies to an agent exactly as it applies to a person: an agent
acts through a Kubernetes identity (a ServiceAccount, or an OIDC user), and Kubernetes RBAC
decides what it may do.

| Control | What it does | Page |
|---|---|---|
| Verified identity on approvals, rejections and overrides | ValidatingAdmissionPolicies refuse an Approval, a rejection or a gate override that does not name the caller the API server authenticated. Nobody records a decision in someone else's name | [Verified identity](../guides/security.md#verified-identity) |
| The verified creator | Each version's `kardinal.io/created-by` annotation is admitted only in the requester's name and never changes. With TokenReview enabled, the Bundle API records the authenticated caller | [Require approvals](../approvals.md) |
| `excludeAuthor` | An approval gate with `excludeAuthor: true` does not count the version's creator, so whoever created a version, an agent included, cannot approve it | [Require approvals](../approvals.md) |
| Approval quorum | `required: N` distinct approvers from `allowedUsers` and `allowedGroups`; a counted reject blocks | [Require approvals](../approvals.md) |
| CEL gates | PolicyGates hold an environment until a CEL rule passes: soak time upstream, metrics, schedule, PR review state, version metadata | [Gate promotions](../policy-gates.md) |
| ChangeWindows | Cluster-scoped freezes and allowed hours, enforced by the controller for every pipeline whose gates read them; breaking glass is a verified, time-limited, audited override | [ChangeWindow attributes](../policy-gates.md#changewindow-attributes), [Override a gate](../overrides.md) |
| PR evidence | Every pull request kardinal opens carries the version's provenance and each gate's result and reason | [Read PR evidence](../pr-evidence.md) |
| Audit trail | Append-only `AuditEvent` records of promotions, rollbacks, supersession, gate results, approvals (with the approver), overrides (with who overrode) and rejections (with who rejected) | [Audit logging](../guides/security.md#audit-logging) |

## A starting point for an agent

Give the agent its own ServiceAccount, bound in the Pipeline namespace to the chart's
`<release>-promoter` ClusterRole, which may create versions but not Approvals
([User roles](../guides/security.md#user-roles)), and gate prod on people:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: prod-needs-a-person
  namespace: my-app
  labels:
    kardinal.io/applies-to: prod
spec:
  expression: "true"
  message: "prod needs a release manager who did not create the version"
  approval:
    required: 1
    allowedGroups: [release-managers]
    excludeAuthor: true
```

A version the agent ships with its own kubeconfig (`kardinal ship my-app --image ...`) records the
agent's ServiceAccount as its creator. It promotes through test and uat under the other gates,
and waits at prod for a release manager, who cannot be the agent. Restrict who may push to
the GitOps repository, so that changes reach the environments through kardinal, and for
`pr-review` environments require the `kardinal/gates` status check
([Gate status check](../pr-evidence.md#gate-status-check-kardinalgates)). Do not grant the agent `impersonate`: whoever may impersonate passes every
identity check as whoever they impersonate.

## Connect an agent over MCP

`kardinal mcp` is a local MCP server for Claude Code, Codex, Cursor and other agents: the
read tools (`why`, `status`, `simulate` and more) by default, the write tools with
`--allow-write`, all as the agent's own Kubernetes identity. Text from PRs, commits and people
comes labelled as untrusted data. See [Connect an agent](connect-an-agent.md).

## What's next

Agent identities with sponsors, actor-aware policy, agent verdicts as gates and an agent freeze
are planned; they are not in kardinal yet. See the [roadmap](../roadmap.md).
