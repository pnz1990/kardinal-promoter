# Promotion stuck or failed

A version that does not move, a step that waits, a gate that never passes, or an object that
does not delete. Start with [`kardinal doctor` and `kardinal explain`](troubleshooting.md#start-here-kardinal-doctor).

## Promotion is stuck

### Symptom: PromotionStep stays in "Pending"

The PromotionStep exists but has not started. Its `status.message` says why: `waiting for gate <name>` (a required gate is not ready), or `pipeline <name> is paused — resume with: kardinal resume <name>`. An environment the Graph has not reached yet has no PromotionStep at all.

```bash
# Show all steps and gates
kardinal get steps my-app

# Check which gate is blocking
kardinal explain my-app --env prod
```

If the output shows a PolicyGate in Block state, the gate's CEL expression has not been satisfied; REASON shows what it evaluated to. Common causes:
- `no-weekend-deploys`: it is a weekend. Wait for Monday, or record a break-glass override with `kardinal override <pipeline> --stage prod --gate no-weekend-deploys --reason "..."` (see [Emergency Overrides](overrides.md)).
- `staging-soak`: the upstream environment was verified recently. Wait for the soak time to pass.
- CEL error: the expression uses a variable or key that is not in the [CEL context](reference/cel-context.md). Check `kardinal policy test <file>`.

If the step's message is `waiting for gate <name>`, that gate holds it before it starts
(see [When a gate holds a step](policy-gates.md#when-a-gate-holds-a-step)). When the gate's
expression is false and the gate has a `message`, the step's message adds it:
`waiting for gate <name>: <message>`. `waiting for gate <name>
to be re-evaluated` means the gate's last result is older than the step; the controller re-evaluates
it when the step is created, so this clears within seconds. If it does not, check that the
controller is running and that its clock is in sync with the API server.

### Symptom: PromotionStep stays in "WaitingForMerge"

The PR has been opened but not merged. Check:

```bash
# Find the PR URL
kubectl get promotionsteps -l kardinal.io/pipeline=my-app,kardinal.io/environment=prod \
  -o jsonpath='{range .items[*]}{.metadata.name}: {.status.prURL}{"\n"}{end}'
```

Common causes:
- PR needs review (CODEOWNERS, required reviewers)
- CI checks failing on the PR
- The PR was closed without merging. The message then reads `PR #<n> is closed; the step fails 5m0s after closing unless it is reopened`. Reopen the PR within 5 minutes and the step keeps waiting for the merge. The controller never reopens a PR itself.

After those 5 minutes the controller comments on the PR that it no longer tracks it and stops polling it. The step then fails with `PR #<n> was closed without merging and not reopened within 5m0s`. Reopening or merging the PR after that does not resume the promotion, and a merge changes the environment with no PromotionStep tracking it. To promote again, create a new Bundle. The PRStatus shows the state:

```bash
kubectl get prstatus -o custom-columns=NAME:.metadata.name,OPEN:.status.open,CLOSED_AT:.status.closedAt,FINAL:.status.closedFinal
```

The PRStatus polls an open PR every 30 seconds, but `status.lastCheckedAt` is not the time of
the last poll. A poll that finds nothing changed writes nothing, unless `lastCheckedAt` is 5
minutes old; then it refreshes it. This is by design: every status write is an update event
that queues the PRStatus again, so writing on every poll made it poll in a loop, and it would
also add an API server write every 30 seconds for each open PR. A `lastCheckedAt` up to 5
minutes old is normal for an open PR; once a PR is merged or final-closed it stops changing.
With `logLevel: debug`, the controller logs `PR still open, requeueing` with `prstatus` and
`namespace` at every poll of an open PR.

**If the PR was merged but the step is still `WaitingForMerge`**: the PRStatus poll sees the merge within 30 seconds even when no webhook arrived. Check the PRStatus. A `pollError` means the poll fails; see the HTTP 401/403/404 symptom below.

```bash
kubectl get prstatus -l kardinal.io/pipeline=my-app,kardinal.io/environment=prod \
  -o custom-columns=NAME:.metadata.name,MERGED:.status.merged,ERROR:.status.pollError
```

To verify webhook connectivity:

```bash
kubectl port-forward svc/kardinal-promoter -n kardinal-system 8083:8083 &
curl http://localhost:8083/webhook/scm/health
# Returns: {"status":"ok","webhookConfigured":true,"eventsProcessed":N,"mergedPREvents":M}
```

`eventsProcessed` counts every signed event since the controller started, whatever its
type (push, comment, pull request), so any test delivery from your SCM raises it.
`mergedPREvents` counts only merged pull request (merge request) events, the only events
that move a promotion, including those the SCM API did not confirm. The `webhook received`
log line names each event's type in `event_type` and its repository in `repo`, and
`PRStatus marked merged via webhook` names the PRStatus in `prstatus` and `namespace`.

With a secret set, an event the controller refuses gets `401` with a plain-text body, like
the endpoint's other errors, and the controller logs `webhook signature invalid or parse
error` with `signatureHeader`, the header the signature was read from (`none` when the
request had none, usually because the webhook in the SCM has no secret), and `remoteAddr`,
the sender's address (the proxy's, when requests come through an ingress). The signature
itself is not logged.

`webhookConfigured: false` means the `--webhook-secret` flag is not set. The controller
then answers every `POST /webhook/scm` with `401` (it does not accept unsigned events)
and logs `SCM webhooks disabled` once at startup. Promotions still complete: merges are
detected by PR status polling, just more slowly than with webhooks. To enable webhooks, store
the secret in a Secret in the controller's namespace and set the chart's `webhook.secretRef.name`
(key `secret`). Use the same value as the webhook secret in your SCM.

### Symptom: PromotionStep stays in "HealthChecking"

The health adapter has not reported the environment as healthy.

```bash
# Check the PromotionStep status
kubectl get promotionsteps -l kardinal.io/pipeline=my-app,kardinal.io/environment=prod -o yaml
```

`status.message` names the adapter and its last result. `waiting for <adapter>` means the rollout is still in progress; `unhealthy via <adapter>` means the target is not healthy, and `status.consecutiveHealthFailures` counts those checks.

Common causes:
- Argo CD Application has not synced the promoted commit yet (`revision=<old>, waiting for <new>`; check the Application sync status and revision)
- Flux has not applied the promoted commit yet (`lastAppliedRevision=<old>, waiting for <new>`)
- The Flux Kustomization is suspended (`is suspended; Flux applies nothing until it is resumed`; run `flux resume kustomization <name>`)
- The Deployment still runs the previous image (`not updated yet`) or has not finished rolling out
- Deployment pods are crash-looping (check pod logs)
- Health timeout is too short for slow deploys (increase `health.timeout`)
- The health adapter is checking the wrong object (check `health.type` and the `health.resource`, `health.argocd` or `health.flux` override; see [Health Adapters](health-adapters.md))

## Bundle not promoting

### Symptom: Bundle stays in "Available"

The Bundle was created but no Graph was generated. Check that `spec.pipeline` names a Pipeline in the Bundle's namespace, and read the Bundle's conditions:

```bash
kubectl get bundle <name> -o jsonpath='{.spec.pipeline}{"\n"}{range .status.conditions[*]}{.type} {.reason}: {.message}{"\n"}{end}'
```

Reason `PipelineNotFound` means no such Pipeline exists. Reason `WaitingForSlot` means the Pipeline's `maxConcurrentPromotions` Bundles are already promoting; the Bundle starts when one of them finishes. A `Failed` Bundle waits the same way before it recovers: see [spec.maxConcurrentPromotions](reference/pipeline.md#specmaxconcurrentpromotions).

### Symptom: Bundle is Failed with "skip denied"

The Bundle's `intent.skipEnvironments` lists an environment that an org gate applies to, and no skip-permission gate allows the skip. The Bundle's phase is `Failed` and its status conditions say `skip denied for environment "<env>": ...`.

```bash
kubectl get bundle <name> -o jsonpath='{.status.conditions[*].message}'
```

Either remove the environment from `intent.skipEnvironments`, or have the platform team create a skip-permission gate for it (the Failed Bundle is then retried; it promotes unless a newer Bundle of the Pipeline is in flight or Verified): a PolicyGate in an org policy namespace (`--policy-namespaces`, default `platform-policies`) labelled `kardinal.io/type: skip-permission` and `kardinal.io/applies-to: <env>`, with `spec.skipPermission: true`. A gate in the Pipeline's namespace or in `spec.policyNamespaces` cannot grant a skip. See [Skip Permissions](policy-gates.md#skip-permissions).

## Health adapter issues

### Symptom: Argo CD adapter reports "Application not found"

The Application name in `health.argocd.name` does not match an actual Argo CD Application.

```bash
# List Argo CD Applications
kubectl get applications -n argocd

# Check the Pipeline health config
kubectl get pipeline my-app -o yaml | grep -A5 argocd
```

Common causes:
- Typo in the Application name
- Application is in a different namespace (check `health.argocd.namespace`)
- Application has not been created yet (check the Argo CD ApplicationSet)

### Symptom: Flux adapter reports "Kustomization not found"

Same as above but for Flux. Check `kubectl get kustomizations -n flux-system`.

### Symptom: PromotionStep fails with "health.cluster is not supported"

`health.cluster` is a deprecated string field, and an environment that sets it fails instead of checking the local cluster. Remove it. To verify a workload in another cluster, set `health.kubeconfigSecretRef` to a kubeconfig Secret for that cluster, or check its Argo CD Application or Flux Kustomization in the controller's cluster (`health.type: argocd` or `flux`). See [Health Adapters](health-adapters.md#remote-clusters).

## Webhook issues

### Symptom: PRs merged but PromotionStep not advancing

The merge event webhook was not received. Check:

```bash
# Check controller logs for webhook events
kubectl logs -n kardinal-system deploy/kardinal-promoter | grep webhook
```

Common causes:
- Webhook not configured in GitHub (Settings > Webhooks)
- Webhook URL is not accessible from GitHub (firewall, private cluster)
- The git server refuses to deliver to the controller's address: on Forgejo and Gitea it is
  not in `ALLOWED_HOST_LIST`, whose default allows only external hosts; on GitLab
  `allow_local_requests_from_web_hooks_and_services` is off. The controller never sees the
  event. The settings are in SCM Providers for [Forgejo and Gitea](scm-providers.md#webhook-configuration_2) and [GitLab](scm-providers.md#webhook-configuration_1).
- Webhook secret mismatch (`X-Hub-Signature-256` validation failing)
- The SCM API did not confirm the merge. The controller asks it before it marks a PR
  merged, and logs `SCM provider reports the PR of the merge event not merged` or `could not
  confirm the merge event with the SCM provider` with the `prstatus`, `namespace`, `repo` and
  `pr`. The event changes nothing; the next poll, within 30 seconds, sees the merge.

Merges are also found by polling: each PRStatus checks its PR every 30 seconds, and after a restart every PRStatus is polled again. Wait 30 seconds and check again.

### Symptom: "429 Too Many Requests" from the Bundle API

The Bundle API allows 60 requests per minute. There is one token, so every CI job and every Pipeline shares that limit, and every request with the right token counts, including ones rejected with `400`. The window is a fixed minute kept in the controller process.

The limit cannot be changed. Create fewer Bundles (for example one per merge to main rather than one per push), or retry after the minute is over. The create-bundle GitHub Action does not retry a `429`.

## Graph controller issues

### Symptom: Graph shows `Accepted: False` with a CEL compile error

kardinal generates the Graph, so a rejected Graph is a bug in kardinal. The Bundle is `Failed` with reason `GraphRejected` and kro's message: `kubectl describe bundle <name>`. Please open an issue with that message and the Pipeline.

### Symptom: Graph exists but its Ready condition stays False

```bash
# Graph conditions: Accepted, ResourcesConverged, Ready
kubectl get graph -l kardinal.io/bundle=<bundle> -o jsonpath='{range .items[0].status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'

# Check for CRD schema issues
kubectl get crd policygates.kardinal.io -o jsonpath='{.status.conditions}' | python3 -m json.tool

# Verify kro is running
kubectl get pods -n kro-system
```

If kro is in CrashLoopBackOff:
```bash
kubectl describe deployment kro -n kro-system
kubectl logs -n kro-system deployment/kro --previous
```

### Symptom: PromotionStep CRDs are not created even though Graph exists

The Graph controller creates a PromotionStep only after the nodes it depends on are ready
(their `readyWhen` holds). Check the Graph's conditions (`Accepted`, `ResourcesConverged`,
`Ready`):
```bash
kubectl get graph -l kardinal.io/bundle=my-app-v1 -o jsonpath='{.items[0].status.conditions}'
```

If a PolicyGate node is not ready, downstream PromotionSteps will not be created until it passes.

### Symptom: Bundle condition "GatesCreated" is False

kro creates a Bundle's PolicyGate instances from one collection and makes them available to the
promotion only when every instance was created. One instance the API server refuses (a
`ResourceQuota` on `count/policygates.kardinal.io`, an admission policy, throttling) holds every
gated environment of the Bundle. The condition names the missing instances and kro's error:

```bash
kubectl get bundle <name> -o jsonpath='{.status.conditions[?(@.type=="GatesCreated")].message}'
```

Fix what refuses the instance (raise the quota, allow it in the policy); kro retries on its own and
the Bundle continues. A PRStatus that cannot be created holds only its own environment: its
PromotionStep waits in `WaitingForMerge` with `waiting for PRStatus <name>: the Graph has not
created it yet`.

### Symptom: Bundle condition "RunsCreated" is False

In a compact Graph, kro creates the HookRuns and AnalysisRuns from collections. The API server
refused one of them, for example because of a `ResourceQuota`, an admission policy or an invalid
Job. That run's environment waits: its step reports that it is waiting for the hook or the
analysis. The other environments go on. The condition names the run, its environment and kro's
error:

```bash
kubectl get bundle <name> -o jsonpath='{.status.conditions[?(@.type=="RunsCreated")].message}'
```

Fix what refuses the run. kro retries on its own, and the condition goes away once kro no longer
reports the run.

## PolicyGate never becomes Ready

### Symptom: PolicyGate stays Block or Waiting, or shows "CEL error"

```bash
# Check the gate's per-Bundle instances (the template itself is never evaluated)
kubectl get policygates -A -l kardinal.io/gate-template=my-gate -o custom-columns=NAME:.metadata.name,READY:.status.ready,REASON:.status.reason

# Show the expression and current evaluation
kardinal explain my-app --env prod
```

**Unknown attribute:** `kardinal policy test my-gate.yaml` names the attribute that is missing. Only the attributes in the [CEL context reference](reference/cel-context.md) exist; anything else (for example `delegation.status`, `externalApproval.*`, `previousBundle.*` or `bundle.metadata.*`) is an error and the gate blocks. `metrics.<name>` exists only when a `MetricCheck` named `<name>` exists in the gate's metrics namespace (the org policy namespace for an org gate, the Pipeline namespace otherwise), and `upstream.<env>` only when that environment appears in the Bundle's status or recent history.

**CEL syntax error:** The expression failed to compile. Common mistakes:
- Calling a field as a function: `!schedule.isWeekend` (correct) vs `!schedule.isWeekend()` (fails with `undeclared reference to 'isWeekend'`: it is a map field, not a function)
- Unknown variable: `bundle.version` (correct) vs `bundle.spec.images[0].tag` (not in the context; see the [CEL context reference](reference/cel-context.md))
- Type mismatch: comparing string to int without casting

Test your expression before applying:
```bash
kardinal policy test my-gate.yaml
```

After applying, `kardinal policy simulate --pipeline my-app --env prod --time "Tuesday 10am"` evaluates it at a chosen time.

**No ScheduleClock:** each ScheduleClock tick re-evaluates every gate. The chart creates `kardinal-clock`, which ticks every minute. Without a ScheduleClock (`scheduleClock.enabled: false`), a gate is re-evaluated only every `spec.recheckInterval` (default 5m, minimum 10s). Check with `kubectl get scheduleclocks -A`.

**Gate expression references an upstream environment that hasn't verified yet:**
```bash
# Check upstream soak minutes — must be > 0 for soak gates to work
kubectl get promotionstep -l kardinal.io/bundle=my-app-v1 -o jsonpath='{range .items[*]}{.metadata.name}: {.status.state}{"\n"}{end}'
```

### Symptom: PolicyGate stays Block even though the condition should pass

```bash
# Force re-evaluation by annotating the gate instances. The controller evaluates
# the per-Bundle instances, which are labelled with their template's name.
kubectl annotate policygate -A -l kardinal.io/gate-template=no-weekend-deploys \
  kardinal.io/force-recheck=$(date +%s) --overwrite

# Or trigger a ScheduleClock tick, which re-evaluates every gate
kubectl annotate scheduleclock kardinal-clock \
  kardinal.io/manual-tick=$(date +%s) -n kardinal-system --overwrite
```

Any annotation change on a PolicyGate or ScheduleClock triggers a reconcile; the two keys
above are conventions. A status-only write does not.

## A PromotionStep, Graph or namespace never finishes deleting

Two finalizers can hold a delete, and the controller removes both itself while it runs.

**`kardinal.io/close-pr` on a PromotionStep.** A step that opens a promotion PR (its environment
was `pr-review` when the step started) carries it while it is `Promoting` or `WaitingForMerge`,
from before it opens the PR; an `auto` step never carries it. When the step is deleted, the controller asks the SCM whether
the PR is still open, closes it with a comment if it is, and deletes its head branch
(`kardinal/<namespace hash>/<bundle>/<env>`) so the closed PR cannot be merged later; a merged PR is left alone,
and a closed one only loses its branch. The branch is kept when the step comes back and pushes it
again at once (the PromotionStep alone, below). Then it removes the finalizer. What happens to the PR depends on what was deleted:

- **The Bundle.** The PR is closed with the comment `kardinal closed this PR: bundle <bundle>
  was deleted. ...`.
- **The namespace.** The PR is closed with the comment `kardinal closed this PR: namespace
  <namespace> was deleted. ...`.
- **The PromotionStep alone** (`kubectl delete promotionstep`). kro creates the step again, under
  the same name, once the old one is gone. The old PR is closed first (`kardinal closed this PR:
  PromotionStep <name> was deleted. ...`), and the new step opens a new PR. The step's PRStatus
  then names the new PR (`spec.prNumber`), and the old PR's status is cleared before the new PR
  is polled (`status.observedGeneration` catches up with `metadata.generation`).
  The old PR keeps its branch while the Bundle is `Promoting` or `Failed`, its Graph is still
  there, and the new step pushes at once. The controller logs `kept the head branch of the
  closed PR`. The new step pushes the same branch about a second later. Forgejo and Gitea close
  every open PR of a deleted branch from a queue after the delete returns, so deleting the
  branch closed the new PR too. The new step pushes at once when kro accepted the Graph, every
  required gate of the step is ready, the Pipeline is not paused, the upstream steps are
  `Verified`, and the controller supports the step's configuration. Otherwise the new step would
  wait or not come, so the controller deletes the branch. On GitHub the closed old PR can still
  be merged through the API while its branch is kept, with the same Bundle's change for the same
  environment. The branch goes when the controller closes the new step's PR. If the new step ends
  before it opens a PR (its Bundle is superseded, the step fails, or it is deleted after it
  started and no step after it pushes at once), the controller deletes the branch then; **A
  branch left with no PR** below lists when it stays.
- **The Graph, while the Bundle is `Promoting`.** The PR stays open: the controller recreates the
  Graph, and the new step reuses the PR. The controller logs `left the PR of a step deleted with
  its Graph open` with the `env` and `prURL`.

If the SCM call keeps failing, the controller retries with backoff for about 5 minutes, then
removes the finalizer anyway and logs the error `gave up closing the PR of a deleted
PromotionStep; removing its finalizer` with the `env` and `prURL`: close that PR by hand, since
merging it would change the environment with no PromotionStep tracking it. When the PR was closed
but its branch could not be deleted, the error says `PR #<n> is closed, but deleting its branch
kardinal/<namespace hash>/<bundle>/<env> failed` (for a step with no PR, `the step opened no PR, but deleting its
branch kardinal/<namespace hash>/<bundle>/<env> failed`): delete that branch by hand. It also emits a
`ClosePRFailed` Warning Event on the step, except in a namespace being deleted: the API server
refuses new Events there, and the step is gone, so the controller log is the only record.
`kardinal_pr_cleanup_failures_total{reason="ClosePRFailed"}` (and `reason="PRLeftOpen"` for the
case below) counts these PRs, so alert on it: Warning Events are rate-limited and best effort
([Kubernetes Events](operate.md#kubernetes-events)).

Before it closes the PR, the controller reads the Bundle, its namespace, its Pipeline and its
Graph to tell whether the step comes back (the Graph case above). If one of those reads keeps
failing, it retries for about 5 minutes, then removes the finalizer without closing or
commenting on the PR: a new step may still come back and reuse it, and leaking an open PR is
safer than closing one that step owns. It logs the error `gave up telling whether a deleted
PromotionStep comes back; left its PR open and removed its finalizer` with the `env` and
`prURL`, and emits a `PRLeftOpen` Warning Event on the step (not in a namespace being deleted,
as above). If no PromotionStep uses that PR, close it by hand, as below.

**A PR left open with no PromotionStep.** In the Graph case above, the PR stays open on the
promise that a new step reuses it. If the Bundle is deleted or stops `Promoting`, or its
namespace is deleted, before the new step exists, nothing tracks the PR and the controller never
closes it. (A Graph that fails to translate leaves the PR open only until the translation works
again; the new step then reuses it.) Such a PR has the `kardinal/promotion` label and the branch
`kardinal/<namespace hash>/<bundle>/<environment>`, and no PromotionStep matches it:

```bash
kubectl get promotionsteps -n <namespace> -l kardinal.io/bundle=<bundle>,kardinal.io/environment=<environment>
```

Close it by hand. The `left the PR of a step deleted with its Graph open` log line names it.
If a new step for that Bundle and environment ends before it opens a PR, it deletes the branch
(below), and the SCM closes such a PR with no comment from kardinal.

**A branch left with no PR.** A step that opens a PR but ends before it opens one (it is
superseded, fails, or is deleted) deletes its `kardinal/<namespace hash>/<bundle>/<environment>` branch, because
`git-push` may have pushed it, or a deleted step may have kept it for this one. The branch can
still be left in a few cases:

- The Pipeline is gone, so nothing names the repository.
- A gate stopped being ready, or the Pipeline was paused, in the second between the delete of a
  step that kept the branch and kro creating the step again. The new step then waits in
  `Pending`, and a `Pending` step holds no finalizer, so deleting it (or its Bundle) leaves the
  branch.
- The environment changed from `pr-review` to `auto` before that new step started.

Such a branch has no open PR, and no PromotionStep for its Bundle and environment is
`Promoting` or `WaitingForMerge` (use the `kubectl get promotionsteps` command above). It holds
only that Bundle's change for that environment, so deleting it by hand changes nothing deployed.

The step stays only while the controller is not running, for example after `helm uninstall`
without deleting the Bundles first. The controller did not close its PR: close the PR by hand,
then remove the finalizer (to remove it from every step at once, see
[Uninstall](installation.md#uninstall)):

```bash
# Deleted steps still holding a finalizer
kubectl get promotionsteps -A -o jsonpath='{range .items[?(@.metadata.deletionTimestamp)]}{.metadata.namespace}{" "}{.metadata.name}{" "}{.status.prURL}{" "}{.metadata.finalizers}{"\n"}{end}'

# Remove kardinal.io/close-pr (the test op makes the patch fail if index 0 holds another finalizer)
kubectl patch promotionstep <name> -n <namespace> --type=json -p \
  '[{"op":"test","path":"/metadata/finalizers/0","value":"kardinal.io/close-pr"},{"op":"remove","path":"/metadata/finalizers/0"}]'
```

**`kro.run/graph-finalizer` on a Graph in a namespace being deleted.** kro deletes a Graph's
resources as the Graph ServiceAccount, which the applier RoleBinding authorizes. Deleting the
namespace deletes that RoleBinding too, after which every delete kro makes is forbidden and it
keeps its finalizer. The controller removes kro's finalizer from its own Graphs (the
`kardinal.io/bundle` label and a Bundle owner) once the namespace is Terminating and the applier
RoleBinding is gone; the namespace deletion then deletes the resources. It logs `removed kro's
finalizer from a Graph in a terminating namespace`. A Graph whose teardown was already stuck
before the namespace deletion started is found within 30 seconds of it: the controller checks a
deleting Graph again every 30 seconds until its namespace is Terminating. It never touches a
Graph outside a Terminating namespace, or one kardinal did not create.

If the controller is not running, check that the namespace is Terminating, then remove the
finalizer by hand:

```bash
kubectl get namespace <namespace> -o jsonpath='{.status.phase}'   # Terminating
kubectl get graphs.kro.run -n <namespace> -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.metadata.finalizers}{"\n"}{end}'
kubectl patch graphs.kro.run <name> -n <namespace> --type=json -p \
  '[{"op":"test","path":"/metadata/finalizers/0","value":"kro.run/graph-finalizer"},{"op":"remove","path":"/metadata/finalizers/0"}]'
```
