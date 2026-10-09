# Troubleshooting

Common problems and how to diagnose them.

## Admission validation errors

The kardinal CRDs validate their fields with OpenAPI schema rules, so `kubectl apply`
rejects a bad value before it is stored. The error names the field and the rule it broke.
Common cases:

| Field | Fix |
|---|---|
| `spec.environments` | Add at least one environment to your Pipeline spec |
| `spec.environments[].name` | Use a non-empty, valid environment name; the error names the rule it broke |
| `spec.environments[].update.strategy` | Only `kustomize`, `helm`, `argocd` are valid |
| Bundle `spec.type` | Only `image`, `config`, `mixed` are valid |
| Bundle `spec.pipeline` | Add `pipeline: <name>` to your Bundle spec |
| PolicyGate `spec.expression` | Add a CEL expression to your PolicyGate spec |
| PolicyGate `spec.recheckInterval` | Use Go duration format: `5m`, `30s`, `1h` (not `5 minutes`) |

These checks are part of the CRDs and cannot be turned off. The chart's only
ValidatingAdmissionPolicies are the identity policies (a rejection, an override or an approval
must name the requesting user, see [Verified identity](guides/security.md#verified-identity)); an
`admission webhook "..." denied the request` or `ValidatingAdmissionPolicy '<release>-...' ... denied
request` error names the rule. The `validatingAdmissionPolicy.enabled` value has no effect.

---

## Start here: `kardinal doctor`

Before diving into specific issues, run the pre-flight check to rule out common
configuration problems:

```bash
kardinal doctor
```

This checks: controller reachability (the `kardinal-version` ConfigMap), CRDs, the kro
controller and Graph CRD, and that `GITHUB_TOKEN` is set on the controller Deployment.
If any check fails, the output includes a remediation hint.

If kardinal-promoter is installed in a namespace other than `kardinal-system`, pass it:
`kardinal doctor --controller-namespace <namespace>`.

For a specific pipeline: `kardinal doctor --pipeline my-app` (in the current namespace, or `-n`).

---

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
- `no-weekend-deploys`: it is a weekend. Wait for Monday, or record a break-glass override with `kardinal override <pipeline> --stage prod --gate no-weekend-deploys --reason "..."` (see [Emergency Overrides](policy-gates.md#emergency-overrides-k-09)).
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

### Symptom: PolicyGate shows "CEL error"

The CEL expression uses a variable or key that is not in the gate's context.

```bash
# Validate the expression
kardinal policy test my-gate.yaml
```

The output will show which attribute is unavailable. Only the attributes in the [CEL context reference](reference/cel-context.md) exist; anything else (for example `delegation.status`, `externalApproval.*`, `previousBundle.*` or `bundle.metadata.*`) is an error and the gate blocks. `metrics.<name>` exists only when a `MetricCheck` named `<name>` exists in the gate's metrics namespace (the org policy namespace for an org gate, the Pipeline namespace otherwise), and `upstream.<env>` only when that environment appears in the Bundle's status or recent history.

## Bundle not promoting

### Symptom: Bundle stays in "Available"

The Bundle was created but no Graph was generated. Check that `spec.pipeline` names a Pipeline in the Bundle's namespace, and read the Bundle's conditions:

```bash
kubectl get bundle <name> -o jsonpath='{.spec.pipeline}{"\n"}{range .status.conditions[*]}{.type} {.reason}: {.message}{"\n"}{end}'
```

Reason `PipelineNotFound` means no such Pipeline exists. Reason `WaitingForSlot` means the Pipeline's `maxConcurrentPromotions` Bundles are already promoting; the Bundle starts when one of them finishes. A `Failed` Bundle waits the same way before it recovers: see [spec.maxConcurrentPromotions](pipeline-reference.md#specmaxconcurrentpromotions).

### Symptom: Bundle is Failed with "skip denied"

The Bundle's `intent.skipEnvironments` lists an environment that an org gate applies to, and no skip-permission gate allows the skip. The Bundle's phase is `Failed` and its status conditions say `skip denied for environment "<env>": ...`.

```bash
kubectl get bundle <name> -o jsonpath='{.status.conditions[*].message}'
```

Either remove the environment from `intent.skipEnvironments`, or have the platform team create a skip-permission gate for it (the Failed Bundle is then retried; it promotes unless a newer Bundle of the Pipeline is in flight or Verified): a PolicyGate in an org policy namespace (`--policy-namespaces`, default `platform-policies`) labelled `kardinal.io/type: skip-permission` and `kardinal.io/applies-to: <env>`, with `spec.skipPermission: true`. A gate in the Pipeline's namespace or in `spec.policyNamespaces` cannot grant a skip. See [Skip Permissions](policy-gates.md#skip-permissions).

## Git errors

### Symptom: "waiting for its turn to push to main: other promotions of this controller are writing it"

Not an error. Automatic promotions of one controller that write the same
branch of the same repository take turns: one clones, commits and pushes at a
time, and the others wait without cloning (#1578). A wide wave of environments
on one branch makes about one push per environment instead of racing for the
branch. A waiting step uses no retry and gives its worker back, at low priority,
so steps of other Pipelines and namespaces are not held up behind the wave
(#1577). Turns go oldest first: when one ends, the oldest waiting step is woken at once
and the others keep their place (each asks again after about its place in line
times the length of a turn, at most 15 seconds). The turn is per repository
branch, whatever the namespace: two teams whose Pipelines write one branch take
turns, because their pushes would collide. PR-review promotions push to their
own `kardinal/` branches and do not wait.

### Symptom: "base branch ... moved while promoting"

In an `approval: auto` environment, something else (another Pipeline, Renovate, Dependabot, a CI job) pushed to the base branch while the step was promoting. `git-push` first replays the promotion's files onto the new head and pushes again, up to 6 times. When the other writer changed the same files, the branch keeps moving, or the shallow clone lacks the commit the promotion was made on (`the clone lacks the commit to rebase from`), the step starts again from a fresh clone, up to 3 times in one reconcile. After that the message reads `(gave up after 3 restarts in this reconcile)` and the step is retried with a jittered backoff (at most 2 minutes), so writers that collided do not collide again: `retrying in <delay> (3, no limit while other writers keep moving the branch)`. These retries are counted in `status.contendedRetries`, not `status.retryCount`: contention alone never fails the step, it only slows it down. `pr-review` environments push to their own `kardinal/<namespace hash>/<bundle>/<env>` branch and do not hit this.

### Symptom: "push ... was refused as non-fast-forward, but the remote branch did not move"

The git server refused the push, yet the base branch is still at the commit the promotion was built on, so no other writer is involved. Common causes are a stale lock file in the server's repository, a read-only repository or protected branch rule that the server reports as non-fast-forward, or a full disk on the git server. This is not contention: the step is retried with the normal limit (`status.retryCount`, 5 retries) and then fails. Fix the repository on the git server, then retry the promotion.

### Symptom: "base branch main moved from ... to ...; rebuilt the PR branch"

Not an error. While a `pr-review` PR waits for its merge, the controller compares the base branch head with the commit the promotion was built on (`status.outputs.baseSHA`) every 30 seconds. When the new commits on the base changed a file under the environment's path (or a Helm `valuesFile` outside it), or the base was force-pushed, it runs the promotion's steps again from a fresh clone of the new head and force-pushes the PR branch. The PR keeps its number; `status.outputs.prBranchRebuilds` counts the rebuilds and the step emits a `PRBranchRebuilt` Event. A base that moved at other paths only updates `baseSHA`. A host that dismisses approvals on a new push (GitHub's "Dismiss stale pull request approvals") asks for the review again after a rebuild. When a rebuild fails, the message reads `rebuilding its branch on main at <sha> failed, retrying` and the PR stays as it was.

### Symptom: "its branch has commits kardinal did not push ... so it is not rebuilt"

Someone pushed to the PR branch (`kardinal/<namespace hash>/<bundle>/<env>`) after kardinal did, so its head is no longer `status.outputs.pushedSHA`. kardinal never overwrites such a commit: the PR is not rebuilt on the moved base. Merge it as it is, or resolve it by hand.

### Symptom: "authentication required" or "authorization failed" on git clone or push

The git token is invalid, expired, or lacks write permissions.

git uses the Secret named by the Pipeline's `spec.git.secretRef` (key `token`), in the Pipeline's namespace. PR and API calls use the controller's token, set with the chart's `github.secretRef` in the controller's namespace. The controller reloads it when the Secret changes.

```bash
# Which Secret does git use?
kubectl get pipeline my-app -o jsonpath='{.spec.git.secretRef.name}'

# Verify the token works (from your machine)
curl -H "Authorization: token $(kubectl get secret <secret> -o jsonpath='{.data.token}' | base64 -d)" \
  https://api.github.com/repos/<owner>/<repo>
```

The token needs repo write access (for GitHub PATs: `Contents: Read and write`, `Pull requests: Read and write`).

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

Remote-cluster health checks through a kubeconfig Secret are not implemented, so an environment that sets `health.cluster` fails instead of checking the local cluster. Remove `health.cluster`. To verify a workload in another cluster, check its Argo CD Application in the controller's cluster (`health.type: argocd`). See [Health Adapters](health-adapters.md#remote-clusters).

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

### Symptom: Graph CR created but no PromotionSteps appear

The Graph controller is not reconciling. Check:

```bash
# Is the Graph controller running?
kubectl get pods -n kro-system

# Check Graph status
kubectl get graph -l kardinal.io/bundle=<bundle> -o yaml
```

If the Graph controller is not running, PromotionSteps will not be created. kardinal-promoter requires the Graph controller to be operational.

### Symptom: Graph shows "Accepted: False"

kardinal generates the Graph, so a rejected Graph is a bug in kardinal. The Bundle is `Failed` with reason `GraphRejected` and kro's message:

```bash
kubectl describe bundle <name>
```

Please open an issue with that message and the Pipeline.

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

## Debugging commands

```bash
# Overview of all pipelines
kardinal get pipelines

# Detailed view of steps and gates
kardinal get steps my-app

# Why is an environment blocked?
kardinal explain my-app --env prod

# Redraw every 3 seconds until Ctrl-C
kardinal explain my-app --env prod --watch

# Bundle history and evidence
kardinal history my-app

# List all policy gates
kardinal policy list

# Validate a policy file
kardinal policy test my-gate.yaml

# Simulate a gate evaluation
kardinal policy simulate --pipeline my-app --env prod --time "Saturday 3pm"

# Raw CRD inspection
kubectl get pipelines,bundles,promotionsteps,policygates -o wide
kubectl get graph -l kardinal.io/pipeline=my-app
```

---

## PolicyGate never becomes Ready

### Symptom: PolicyGate stays Block or Waiting, or shows "CEL error"

```bash
# Check the gate's per-Bundle instances (the template itself is never evaluated)
kubectl get policygates -A -l kardinal.io/gate-template=my-gate -o custom-columns=NAME:.metadata.name,READY:.status.ready,REASON:.status.reason

# Show the expression and current evaluation
kardinal explain my-app --env prod
```

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

### Symptom: PolicyGate stays FAIL even when condition should pass

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

---

## SCM provider failures

### Symptom: `git push origin <branch>: authorization failed`

The remote refused the push (HTTP 403): the token has no write access to the repository or lacks the required scope. An expired or revoked token gives `authentication required` instead (HTTP 401).

git uses the Secret named by the Pipeline's `spec.git.secretRef` (key `token`), in the Pipeline's namespace. PR and API calls use the controller's token, set with the chart's `github.secretRef` in the controller's namespace. The controller reloads it when the Secret changes.

```bash
# Which Secret does git use?
kubectl get pipeline my-app -o jsonpath='{.spec.git.secretRef.name}'

# Verify token scope — must have 'repo' scope (or 'contents:write' for fine-grained tokens)
# Test the token directly:
TOKEN=$(kubectl get secret <secret> -o jsonpath='{.data.token}' | base64 -d)
curl -s -H "Authorization: token $TOKEN" https://api.github.com/user | jq .login
```

To rotate the token, update the Secret in place (here `github-token` in the Pipeline's namespace):
```bash
kubectl create secret generic github-token \
  --from-literal=token=<new-token> \
  --dry-run=client -o yaml | kubectl apply -f -
```

A step that returns an error is retried with backoff (10s, 20s, 40s, 80s, then 2m) up to 5 times; `status.message` shows `retrying in <d> (<n>/5)` and `status.nextRetryAt` when the retry runs. The step waits for it even when something else reconciles it first, such as a gate re-evaluation or a PR status change. Rotate the token within that window and the step continues. After the last retry the PromotionStep is Failed; create a new Bundle to promote again.

### Symptom: "authentication required" with "git Secret ... not found" or "spec.git.secretRef is not set"

git has no token for the Pipeline's HTTPS remote: `spec.git.secretRef` names a Secret that does not exist (or has no `token` key), or is not set. The git error is followed by what is missing:

```
retrying in 20s (2, no limit while git has no credentials) after error: step git-push: git push origin main: authentication required: Unauthorized (git Secret team-a/github-token not found)
```

The PromotionStep's `GitCredentialMissing` condition is `True` with the same message, and one `Warning` Event with reason `GitCredentialMissing` is emitted. Such a step is not failed after 5 retries: it retries every 2 minutes until git has a token. These retries are counted in `status.gitCredentialRetries`, not in `status.retryCount`, so another error, before or after the Secret exists, still gets its 5 retries. Create the Secret in the Pipeline's namespace (and set `spec.git.secretRef` if it is not set), and the step continues at its next retry:

```bash
kubectl create secret generic github-token -n team-a --from-literal=token=<token>
```

### Symptom: "authentication required" with "git Secret ... could not be read"

The controller could not read the Secret that `spec.git.secretRef` names, for example because its
ServiceAccount may not get Secrets in the Pipeline's namespace (`forbidden`) or the API server
timed out. The message names the Secret and the read error:

```
retrying in 20s (2/5) after error: step git-push: git push origin main: authentication required: Unauthorized (git Secret team-a/github-token could not be read: secrets "github-token" is forbidden: ...)
```

The `GitCredentialMissing` condition is `True` with reason `SecretUnreadable`, with one `Warning`
Event. Creating the Secret does not fix this, so the step keeps the limit of 5 retries and then
fails. Fix the read error (for `forbidden`, the controller's RBAC: the chart grants `get` on
Secrets in every namespace the controller reconciles), then create a new Bundle to promote again.

### Symptom: "get PR status failed: HTTP 401", "HTTP 403" or "HTTP 404" on a PromotionStep

`wait-for-merge` fails the step at once, instead of polling, when the SCM API answers:

- **401**: the SCM token was rejected (expired or revoked). Rotate the token as shown above.
- **403** (not a rate limit): the token has no access to the repository or its pull requests.
  Give it read access to pull requests.
- **404 or 410**: the repository or the PR does not exist, or the token cannot see it. Check
  `spec.git.url` on the Pipeline and that the PR was not deleted.

Polling again cannot fix these, so the step is marked as a permanent failure. A 403 rate limit,
429, 5xx or network error keeps the step waiting and it is retried every 30 seconds.

A step already in `WaitingForMerge` learns about the merge from its PRStatus. When the PRStatus
poll gets one of these errors, it writes it to `status.pollError` and the step fails with
`PR #<n> cannot be polled: <error>`:

```bash
kubectl get prstatus -o custom-columns=NAME:.metadata.name,PR:.spec.prNumber,ERROR:.status.pollError
```

The PRStatus keeps polling every 5 minutes and clears `pollError` once a poll succeeds, but the
failed step does not resume. Fix the token or the repository, then create a new Bundle.

### Symptom: "403 rate limit exceeded" or "429 Too Many Requests" in controller logs

GitHub's API rate limit (5000 req/hr for authenticated requests) or GitLab's rate limit has been hit. The **SCM circuit breaker** (shipped in v0.7.0) handles this automatically.

**How the circuit breaker works:**
1. Each repository owner (GitHub user or org, GitLab top-level group, Bitbucket workspace, Azure DevOps organization) has its own circuit. After 5 consecutive failures (5xx or a network error) for that owner's repositories, its circuit opens and its SCM calls are blocked for a cooldown period. Calls for other owners go on
2. An exhausted rate limit (`X-RateLimit-Remaining: 0` or `RateLimit-Remaining: 0`, a 429, or a 403 with `Retry-After`) belongs to the token, so it opens one shared circuit at once and every owner waits for the reset
3. The first cooldown is 5 seconds. Each failed probe doubles it (5, 10, 20, 40, 80 s), up to 2 minutes. Requests that were in flight when the circuit opened, and fail or succeed afterwards, are not counted. The cooldown respects `X-RateLimit-Reset`, `RateLimit-Reset` and `Retry-After` response headers when present
4. After the cooldown, one probe request is allowed (half-open state). Other callers are told to look again in 2 seconds. A probe that gets no answer frees the slot after 45 seconds (the providers' 30-second HTTP timeout and a margin). **Recovery time:** the circuit closes on the first probe after the SCM is back, so promotions resume within the cooldown in force when the outage ended, plus up to a fifth more for the steps' jitter. For a 60-second outage that cooldown is 40 seconds, so steps resume within about 50 seconds of the recovery. However long the outage, they resume within 2.5 minutes
5. On probe success, the circuit closes and normal operation resumes
6. A token rotation (a new value in the token Secret) keeps the circuits: the new token is first used by the probe
7. A step that meets an open circuit waits for it and runs again. The wait does not count against the step's 5 retries: its message says "waiting ... for the SCM (not counted as a retry ...)", the condition `SCMUnavailable` is True, `status.scmWaitSince` says since when, and one `SCMUnavailable` Warning Event is written. The wait is bounded: the step fails ("the SCM was unavailable for ...") once it has waited the environment's `stepTimeoutSeconds`, or else the controller's `--scm-wait-timeout` (default 30 minutes). A superseded step closing its PR waits the same way, so its PR and `kardinal/` branch are not left behind, and after the bound it spends its close retries. PRStatus polls wait for the circuit too
8. Once the bound fails a step, `status.scmWaitSince` is cleared and `SCMUnavailable` is False with reason `TimedOut`
9. Metrics: `kardinal_scm_circuit_state{provider,owner}` is `0` closed, `1` half-open, `2` open, with `owner="_quota"` for the token's rate-limit circuit, and `kardinal_scm_requests_total{result="circuit_open"}` counts the calls an open circuit refused ([SCM API and git metrics](guides/monitoring.md#scm-api-and-git-metrics))

**Checking circuit state in logs:**

```bash
# Look for SCM calls the open circuit blocked
kubectl logs -n kardinal-system deploy/kardinal-promoter | grep "SCM circuit open"

# The error the blocked call returns, also shown in PromotionStep messages:
# github scm: SCM circuit open until 2026-04-17T05:30:00Z
```

**Manual recovery if circuit stays open too long:**

```bash
# Restart the controller to reset in-memory circuit state
kubectl rollout restart deployment/kardinal-promoter -n kardinal-system
```

**Check current GitHub rate limit:**

```bash
# The controller's token: the Secret named by the chart's github.secretRef.name
TOKEN=$(kubectl get secret github-token -n kardinal-system -o jsonpath='{.data.token}' | base64 -d)
curl -s -H "Authorization: token $TOKEN" https://api.github.com/rate_limit | jq .rate
```

**Long-term fix:** Use a GitHub App token (higher rate limits than PAT).

### Symptom: Push succeeds but PR is not opened

Check the controller logs for the PR creation call:
```bash
kubectl logs -n kardinal-system deploy/kardinal-promoter | grep "open-pr\|pull_request" | tail -20
```

Common causes:
- The base branch (`spec.git.branch`, default `main`) does not exist in the GitOps repo
- The environment already runs this version. `git-commit` finds nothing to change, so no PR is opened, and the PromotionStep has `status.outputs.noChanges: "true"`
- The GitOps repo is private and the token lacks `repo` scope

---

## RBAC debugging

### Symptom: "forbidden: User ... cannot list resource ... in API group ..."

The controller ServiceAccount lacks a required RBAC permission.

```bash
# Check what the controller can do
kubectl auth can-i --list \
  --as=system:serviceaccount:kardinal-system:kardinal-promoter

# Check for RBAC errors in logs
kubectl logs -n kardinal-system deploy/kardinal-promoter | grep -i "forbidden\|permission"
```

The Helm chart installs a ClusterRole with all required permissions. If you customized RBAC or installed in a restricted namespace, re-apply the Helm chart:
```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  --namespace kardinal-system --reset-then-reuse-values
```

### Symptom: Team cannot create PolicyGates in another team's namespace

kardinal does not create RBAC for users. Your cluster's Roles decide who may write PolicyGates in each namespace. Check with:
```bash
kubectl auth can-i create policygates.kardinal.io -n platform-policies --as <user>
```

---

## kro Graph controller issues

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

---

## Performance tuning (large-scale deployments)

### 50+ environments / 100+ concurrent Bundles

The controller handles each Bundle independently via a dedicated Graph. For very large deployments, consider:

**1. Raise the controller's resource limits.** Extra replicas do not add throughput: the
controller runs with `--leader-elect`, so only one replica reconciles and the others are
standbys (`replicaCount`, default 1).
```yaml
# values.yaml
resources:
  limits:
    cpu: "2"
    memory: 2Gi
  requests:
    cpu: 500m
    memory: 512Mi
```

**2. Reconcile concurrency** is controller-runtime's default of one worker per CRD type
(MetricCheck uses 4); there is no flag to change it.

**3. Reduce ScheduleClock tick frequency** if no gate needs minute-level `schedule.*`
re-evaluation. The chart owns the `kardinal-clock` ScheduleClock, so set it through Helm:
```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  -n kardinal-system --reset-then-reuse-values --set scheduleClock.interval=5m
```

**4. Bundle history** — finished Bundles are garbage-collected per Pipeline. Set
`spec.historyLimit` on the Pipeline to keep fewer (see
[Pipeline reference](pipeline-reference.md#spechistorylimit)).

**5. Monitor controller performance:**
```bash
# Check reconcile queue depth (via Prometheus if PrometheusRule is installed)
kubectl port-forward svc/kardinal-promoter -n kardinal-system 8080:8080
curl -s http://localhost:8080/metrics | grep "^workqueue_depth"

# Or use the built-in Prometheus alerts (install with --set prometheusRule.enabled=true)
kubectl get prometheusrule kardinal-promoter -n kardinal-system -o yaml
```
