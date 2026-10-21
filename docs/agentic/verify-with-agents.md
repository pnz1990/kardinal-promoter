# Verify with agents

!!! info "New in v0.12.0"
    v0.11.0 and earlier do not have this. The [changelog](../changelog.md) lists every v0.12.0 change.

A **verify gate** holds an environment until verifiers have tested the version where it already
runs, usually the environment before. A verifier is an agent, a person or a CI job. Each one
writes a **Verdict**: `pass`, `fail` or `inconclusive`, with a summary, an evidence link and
findings. The gate counts the Verdicts against its policy, in the open: how many must pass, how
many fails it tolerates, what an inconclusive result does, how long it waits.

Agents may verify only when a gate lists them, and never verify their own party's work: an
agent that wrote the version, the person who sponsors that agent, and that person's other agents
do not count ([the one-party rule](../actors.md)).

Verifiers run in one of two places:

- **Outside the cluster.** An agent in CI, or a desktop agent, tests the environment and records
  its verdict with [`kardinal verify`](#record-a-verdict-from-anywhere).
- **In the cluster.** The gate runs an admin-registered agent image as an
  [AgentRun](#run-agents-in-the-cluster): a Job that runs as its own ServiceAccount (create its Verdict; read the namespace under test), no git credentials, a
  network policy and a deadline, whose Pod writes one Verdict.

The design, with the options considered, is in [Verdicts](../design/19-verdicts.md).

## A verify gate

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: prod-needs-exploratory-qa
  namespace: platform-policies
  labels:
    kardinal.io/applies-to: prod
spec:
  expression: "true"
  message: prod needs two passing exploratory QA runs in uat
  verify:
    check: exploratory-qa      # the verification the gate waits for
    environment: uat           # where the version runs; default: prod's only direct upstream
    required: 2                # passing votes needed (default 1)
    maxFailures: 0             # failing votes tolerated (default 0: any fail blocks)
    allowedAgents: [qa-explorer]
    allowedUsers: []           # people and bots; both lists empty: anyone who is not an agent
    allowedGroups: []
    excludeAuthor: true        # default: the version's author party never counts
    onInconclusive: retry      # hold | retry | fail (default hold)
    retries: 1                 # with retry: further attempts per AgentRun
    timeout: 2h                # from the version's health check in uat; "" waits for ever
    onTimeout: fail            # fail | allow (default fail)
    run:                       # optional: run verifiers in the cluster
      template: qa-explorer    # a StepTemplate
      count: 3                 # AgentRuns per version (default: required)
```

The gate's reason says where it stands, everywhere the gate is shown (`kardinal why -o wide`, the
UI, the PR and notifications):

| State | Reason |
|-------|--------|
| Waiting | `waiting for verdicts: 1 of 2 (qa-explorer)` |
| Waiting, an inconclusive retried | `waiting for verdicts: 1 of 2 (qa-explorer); 1 inconclusive, retrying` |
| Verified | `verified: 2 of 2 (qa-explorer ×2)` |
| Failed | `verification failed: qa-explorer: coupon SAVE10 returns 500` |
| Timed out | `verification timed out after 2h: 1 of 2 (qa-explorer)` |
| Timed out, allowed | `verification timed out after 2h, allowed by onTimeout: 1 of 2 (qa-explorer)` |

A gate may have both `approval` and `verify`; both must be met. The expression still decides
first, and an [override](../overrides.md) passes the gate as for any other.

A Bundle build fails with the reason when a verify gate could never pass: it verifies the
environment it gates, an environment that is not upstream of it, or an environment this Bundle
skips; or it has no `environment` and its environment has no single direct upstream.

## How verdicts count

A Verdict counts for a gate when it is for this very version (its Bundle UID), the gate's
Pipeline, environment and check, and:

1. **its writer may verify**: an agent only when it is in `allowedAgents`; anyone else when listed
   in `allowedUsers` or `allowedGroups`, or when both are empty;
2. **it is not the author's party**: with `excludeAuthor` (the default), the version's author,
   the author's agent and that agent's sponsor never count. An agent's verdict on its own party's
   work never counts, whatever `excludeAuthor` says;
3. **it is the voter's latest**: each **voter** has one vote, its latest Verdict. An AgentRun's run
   is a voter (admission proves the Verdict came from that run's Pod), so `count: 3` runs are
   three votes. Anyone else votes once per party, however many Verdicts they write;
4. **it was written before the timeout**.

Then: more than `maxFailures` failing votes fail the gate; at least `required` passing votes
verify it; past the timeout it fails or allows as `onTimeout` says; otherwise it waits.
`inconclusive` waits with `hold` and `retry`, and is a failing vote with `fail`.

Patterns:

| Want | `verify` |
|------|----------|
| One agent must pass | `required: 1, run: {count: 1}` |
| 2 of 3 runs, one bad run tolerated | `required: 2, maxFailures: 1, run: {count: 3}` |
| A flaky agent: retry inconclusive once | `onInconclusive: retry, retries: 1` |
| An agent and a person must both pass | two gates: one with `allowedAgents`, one with `allowedUsers` |
| Explore for up to an hour, then do not hold the release | `timeout: 1h, onTimeout: allow` |

A fail is never retried: tolerating fails is explicit, with `maxFailures`.

`verdicts.*` exposes the counts to the expression: `verdicts.passed`, `verdicts.failed`,
`verdicts.inconclusive`, `verdicts.required`, `verdicts.verifiers`, `verdicts.timedOut` and
`verdicts.findings.high` (high or critical findings in counted votes), for example
`expression: "verdicts.findings.high == 0"` ([CEL context](../reference/cel-context.md)).

## Record a verdict from anywhere

```bash
kardinal verify checkout uat --check exploratory-qa --pass \
  --summary "checkout, refunds and coupons work" --evidence https://ci.example.com/runs/88123

kardinal verify checkout uat --check exploratory-qa --fail \
  --summary "coupon SAVE10 returns 500" --finding "high:coupon SAVE10 returns 500"
```

`kardinal verify <pipeline> <env>` records the verdict on the version that runs in `<env>`;
`kardinal verify <bundle> --env <env>` names the version. The verdict is a Verdict object in your
Kubernetes name and groups (a SelfSubjectReview), with your [actor stamp](../actors.md); the chart's
admission policy refuses one in anyone else's name. The command then says whether each gate that
waits for the check counts it. `--revoke` deletes your latest verdict;
`kardinal verify list <pipeline|bundle>` lists the verdicts and whether a gate counted each.

Bind the chart's `<release>-verifier` ClusterRole (read, create and revoke your own Verdicts, nothing else) to the
agents and CI jobs that verify; people with `<release>-approver` may verify too. Model, session and
client are recorded as asserted, from `KARDINAL_ACTOR_MODEL`, `KARDINAL_ACTOR_SESSION` and
`KARDINAL_ACTOR_CLIENT`.

## Run agents in the cluster

A cluster admin registers the agent as an [AgentIdentity](../actors.md#register-an-agent) (its
sponsor and scope), and its image as a **StepTemplate** that names it:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: StepTemplate
metadata:
  name: qa-explorer
spec:
  description: Explores the web app like a QA engineer and reports.
  image: ghcr.io/acme/qa-explorer@sha256:4f1c...
  agent: qa-explorer                 # the AgentIdentity its verdicts count for
  timeout: 45m                       # per attempt; default 30m, at most 24h
  env:
    - {name: MODEL_ENDPOINT, value: https://llm.internal.example.com}
  secretEnv:                         # Secrets labelled kardinal.io/step-template: qa-explorer
    - {name: MODEL_API_KEY, secret: qa-explorer-model, key: token}
  egress:                            # besides DNS, the API server and the namespace under test
    - {cidr: 10.20.0.0/16, ports: [443]}
  runtimeClassName: gvisor           # optional: a sandboxed runtime
```

StepTemplates are cluster-scoped: a namespace's admins cannot write one, because a RoleBinding
does not grant a cluster-scoped kind. `--step-template-require-digest` refuses images without a
digest.

When the verified environment's step is Verified, the promotion Graph creates `run.count`
**AgentRuns**, one per run. Before it runs anything, the AgentRun reconciler checks the run is
**bounded**, and fails it with the condition `Bounded=False` and the reason otherwise:

- the StepTemplate is ready, and its agent is ready, not suspended, and its scope covers the
  namespace, the Pipeline and the environment;
- every `secretEnv` Secret is labelled `kardinal.io/step-template: <template>`, so a template
  cannot read a Pipeline's git credentials even by name.

The run does not borrow anyone's RBAC. Kardinal **creates the run's own ServiceAccount**,
`kr-<run>`, and binds it to the chart's two AgentRun ClusterRoles, and to nothing else:

- `<release>-agentrun-writer` in the Pipeline's namespace: create Verdicts, read Bundles;
- `<release>-agentrun-reader` in the namespace under test: read Pods (and their logs), Services,
  Endpoints, EndpointSlices, Events, Deployments, ReplicaSets, StatefulSets, DaemonSets and
  Ingresses. No Secrets, no ConfigMaps, no writes.

When the run ends (or its AgentRun is deleted) kardinal deletes the ServiceAccount, whose tokens
then stop working, and both RoleBindings. The chart's admission policy lets `kr-<run>` write one
Verdict only: the run's own, named after the run, from the run's own Pod (the Pod name in its
token, matched exactly). It cannot vote a second time, for another run, or for another version:
the Graph counts a run's Verdict only for the AgentRun of this Bundle it names, and takes the
run's agent, sponsor, index and attempt from that AgentRun.

Then it creates, owned by the AgentRun:

- a **NetworkPolicy**: no ingress; egress to DNS (kube-dns in kube-system), the API server, every
  Pod of the namespace under test and the template's `egress`, and nothing else. It needs a CNI
  that enforces NetworkPolicy;
- a **Job**: one Pod, never restarted, built by the controller (not from your YAML): the
  template's image, command, args, env and resources; restricted Pod security (non-root,
  read-only root filesystem, no capabilities, RuntimeDefault seccomp); a short-lived projected
  token; and `activeDeadlineSeconds` the timeout.

The namespace under test is the environment's workload namespace (the `resource`, `argoRollouts`
or `flagger` health check's), else the environment's name; `verify.run.namespace` sets it.

### The agent's contract

The Pod gets the run's context as environment variables and as the file
`/kardinal/context.json`:

| Variable | Example |
|----------|---------|
| `KARDINAL_PIPELINE`, `KARDINAL_BUNDLE`, `KARDINAL_BUNDLE_UID`, `KARDINAL_VERSION` | `checkout`, `checkout-1-5-0`, `1b2c...`, `1.5.0` |
| `KARDINAL_ENVIRONMENT`, `KARDINAL_TARGET_NAMESPACE` | `uat`, `checkout-uat` |
| `KARDINAL_CHECK`, `KARDINAL_RUN`, `KARDINAL_ATTEMPT` | `exploratory-qa`, `2`, `1` |
| `KARDINAL_IMAGES` | the version's images, as JSON |
| `KARDINAL_DEADLINE` | when the run times out (RFC 3339) |
| `KARDINAL_CONTEXT` | `/kardinal/context.json` |

The file also holds the **Verdict to write**, complete but for `result`, `summary`,
`evidenceURL`, `findings`, `startedAt` and `durationSeconds`: its name (the AgentRun's), labels,
owner, the run binding and the agent's actor stamp. The agent fills those in and creates it with
its own token, with any Kubernetes client. It writes one Verdict; a second is refused. The exit
code says whether the agent ran, the Verdict what it found: an agent that finds a bug exits 0 and
writes `fail`. A run that ends without a Verdict is retried like an inconclusive one with
`onInconclusive: retry`.

`hack/e2e/testagent` is a minimal agent in the repository (standard library only): it probes the
namespace's Services and writes its Verdict. The live tests run it as a StepTemplate.

### Run once

Each attempt runs at most once. A finished AgentRun never runs its Job again; a Job deleted or
replaced while it runs fails the run; and an AgentRun the Graph creates again after it was deleted
takes its result from its Verdict instead of running (`spec.recorded`). Retries are new attempts
the Graph admits after an attempt that finished without a pass or a fail.

## Evidence

- **Status.** The gate records every Verdict it saw in `status.verdicts`: verifier, result,
  whether it counted and why not, run and attempt, summary, evidence link and findings.
- **PR.** A promotion PR of the gated environment has a `### Verification: <check> in <environment>`
  section with the gate's reason and a row per verdict ([PR evidence](../pr-evidence.md)).
- **UI.** The stage drawer shows a verify gate's quorum and each verdict, with its evidence link
  and the `kardinal verify` command.
- **Audit.** Each verdict that appears in, or leaves, a gate writes a `VerdictRecorded` or
  `VerdictRevoked` AuditEvent naming the verdict's actor and the version's UID (`spec.bundleUID`).
  A verdict that leaves and comes back (its AgentRun deleted and created again, a policy edit)
  is recorded again, so the trail ends on what the gate counts now; a version deleted and
  created again under its name has records of its own.
- **Evidence pack.** `kardinal audit evidence-pack` exports each Verdict with the AgentRun that
  wrote it (`agentruns.ndjson`), so an auditor follows a run's Verdict to its agent and sponsor
  from the pack's records ([Trace a Verdict to its agent](../compliance.md#trace-a-verdict-to-its-agent)).
- **Metrics.** `kardinal_verdicts_total{check,result,verifier_kind}` and
  `kardinal_agentruns_total{template,phase}`. The share of `inconclusive` verdicts of a check is its
  flakiness ([Metrics](../reference/metrics.md)).

## What this does not protect against

- **A compromised verifier.** A verdict is as good as its verifier. Use several agents with
  different sponsors (several gates), and `maxFailures: 0`, for what matters.
- **Prompt injection from the environment under test.** An exploratory agent reads what the
  change under test serves, which could talk it into a pass. kardinal bounds what the agent can
  do, not what it concludes: keep a person's approval on prod.
- **Asserted fields.** Model, session, `startedAt` and `durationSeconds` are what the verifier
  says.
- **A CNI that does not enforce NetworkPolicy.** The policy is created but does nothing. A CNI that
  enforces it a moment after a Pod starts (kindnet: about a second) lets the agent's first
  connections through; the policy exists before the Pod, but the CNI decides when it applies.
- **NetworkPolicies add up.** Another policy in the Pipeline's namespace that allows more egress
  to all its Pods (an allow-all egress policy) also applies to the run's Pod.
- **Edit and admin holders in the Pipeline's namespace** can create Pods or tokens as any
  ServiceAccount there, the run's included: the same trust boundary as for the Graph identity.
