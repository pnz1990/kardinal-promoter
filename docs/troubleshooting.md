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

These checks are part of the CRDs and cannot be turned off. The chart no longer installs a
`ValidatingAdmissionPolicy`, and the `validatingAdmissionPolicy.enabled` value has no effect.

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

The Graph has not yet created this PromotionStep. Check if an upstream step or PolicyGate is blocking.

```bash
# Show all steps and gates
kardinal get steps my-app

# Check which gate is blocking
kardinal explain my-app --env prod
```

If the output shows a PolicyGate in FAIL state, the gate's CEL expression has not been satisfied. Common causes:
- `no-weekend-deploys`: it is a weekend. Wait for Monday, or record a break-glass override with `kardinal override <pipeline> --stage prod --gate no-weekend-deploys --reason "..."` (see [Emergency Overrides](policy-gates.md#emergency-overrides-k-09)).
- `staging-soak`: the upstream environment was verified recently. Wait for the soak time to pass.
- CEL error: the expression references an attribute from a later phase. Check `kardinal policy test <file>`.

If the step exists and its message is `waiting for gate <name>`, that gate holds it before it starts
(see [When a gate holds a step](policy-gates.md#when-a-gate-holds-a-step)). `waiting for gate <name>
to be re-evaluated` means the gate's last result is older than the step; the controller re-evaluates
it when the step is created, so this clears within seconds. If it does not, check that the
controller is running and that its clock is in sync with the API server.

### Symptom: PromotionStep stays in "WaitingForMerge"

The PR has been opened but not merged. Check:

```bash
# Find the PR URL
kubectl get promotionstep my-app-v1-29-0-prod -o jsonpath='{.status.prURL}'
```

Common causes:
- PR needs review (CODEOWNERS, required reviewers)
- CI checks failing on the PR
- The PR was closed without merging. The message then reads `PR #<n> is closed; the step fails 5m0s after closing unless it is reopened`. Reopen the PR within 5 minutes and the step keeps waiting for the merge. The controller never reopens a PR itself.

After those 5 minutes the controller comments on the PR that it no longer tracks it and stops polling it. The step then fails with `PR #<n> was closed without merging and not reopened within 5m0s`. Reopening or merging the PR after that does not resume the promotion, and a merge changes the environment with no PromotionStep tracking it. To promote again, create a new Bundle. The PRStatus shows the state:

```bash
kubectl get prstatus -o custom-columns=NAME:.metadata.name,OPEN:.status.open,CLOSED_AT:.status.closedAt,FINAL:.status.closedFinal
```

**If the PR was merged but the step is still "WaitingForMerge"**: this can happen if the controller was down when the webhook arrived. On next controller restart, startup reconciliation automatically re-checks all in-flight PRs and advances any that were merged during downtime. You can also force a restart:

```bash
kubectl rollout restart deployment/kardinal-promoter -n kardinal-system
```

To verify webhook connectivity:

```bash
kubectl port-forward svc/kardinal-promoter -n kardinal-system 8083:8083 &
curl http://localhost:8083/webhook/scm/health
# Returns: {"status":"ok","webhookConfigured":true,"eventsProcessed":N}
```

`webhookConfigured: false` means the `--webhook-secret` flag is not set. The controller
then answers every `POST /webhook/scm` with `401` (it does not accept unsigned events)
and logs `SCM webhooks disabled` once at startup. Promotions still complete: merges are
detected by PR status polling, just more slowly than with webhooks. To enable webhooks, set
`KARDINAL_WEBHOOK_SECRET` in your controller deployment and use the same value as the
webhook secret in your SCM.

### Symptom: PromotionStep stays in "HealthChecking"

The health adapter has not reported the environment as healthy.

```bash
# Check the PromotionStep status
kubectl get promotionstep my-app-v1-29-0-prod -o yaml
```

`status.message` names the adapter and its last result. `waiting for <adapter>` means the rollout is still in progress; `unhealthy via <adapter>` means the target is not healthy, and `status.consecutiveHealthFailures` counts those checks.

Common causes:
- Argo CD Application has not synced the promoted commit yet (`revision=<old>, waiting for <new>`; check the Application sync status and revision)
- Flux has not applied the promoted commit yet (`lastAppliedRevision=<old>, waiting for <new>`)
- The Deployment still runs the previous image (`not updated yet`) or has not finished rolling out
- Deployment pods are crash-looping (check pod logs)
- Health timeout is too short for slow deploys (increase `health.timeout`)
- The health adapter is checking the wrong object (check `health.type` and the `health.resource`, `health.argocd` or `health.flux` override; see [Health Adapters](health-adapters.md))

### Symptom: PolicyGate shows "CEL error"

The CEL expression references an attribute that does not exist in the current phase.

```bash
# Validate the expression
kardinal policy test my-gate.yaml
```

The output will show which attribute is unavailable. Only the attributes in the [CEL context reference](reference/cel-context.md) exist; anything else (for example `delegation.status`, `externalApproval.*`, `previousBundle.*` or `bundle.metadata.*`) is an error and the gate blocks. `metrics.<name>` exists only when a `MetricCheck` named `<name>` exists in the gate's namespace, and `upstream.<env>` only when that environment appears in the Bundle's status or recent history.

## Bundle not promoting

### Symptom: Bundle stays in "Available"

The Bundle was created but no Graph was generated. Check:

```bash
# Is there a Pipeline for this Bundle?
kubectl get pipelines
kubectl get bundle <name> -o yaml | grep kardinal.io/pipeline
```

The `kardinal.io/pipeline` label on the Bundle must match a Pipeline name. If the label is missing or mismatched, the controller ignores the Bundle.

### Symptom: Bundle is Failed with "skip denied"

The Bundle's `intent.skipEnvironments` lists an environment that an org gate applies to, and no skip-permission gate allows the skip. The Bundle's phase is `Failed` and its status conditions say `skip denied for environment "<env>": ...`.

```bash
kubectl get bundle <name> -o jsonpath='{.status.conditions[*].message}'
```

Either remove the environment from `intent.skipEnvironments`, or have the platform team create a skip-permission gate for it: a PolicyGate in an org policy namespace (`--policy-namespaces`, default `platform-policies`) labelled `kardinal.io/type: skip-permission` and `kardinal.io/applies-to: <env>`, with `spec.skipPermission: true`. A gate in the Pipeline's namespace or in `spec.policyNamespaces` cannot grant a skip. See [Skip Permissions](policy-gates.md#skip-permissions).

## Git errors

### Symptom: "push failed: conflict" in controller logs

Another process (or another controller replica) pushed to the same branch between the controller's fetch and push. The controller retries up to 3 times with re-fetch.

If this happens frequently, check:
- Multiple Bundles for the same Pipeline promoting simultaneously (expected, but the controller serializes pushes per repo via mutex)
- External tools (Renovate, Dependabot) writing to the same directories

### Symptom: "authentication failed" in controller logs

The Git token in the Secret is invalid, expired, or lacks write permissions.

```bash
# Check the Secret exists
kubectl get secret github-token

# Verify the token works (from your machine)
curl -H "Authorization: token $(kubectl get secret github-token -o jsonpath='{.data.token}' | base64 -d)" \
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
- Webhook secret mismatch (`X-Hub-Signature-256` validation failing)

On controller restart, the controller lists all open PRs with the `kardinal` label and reconciles any that were merged during downtime. If the controller recently restarted, wait 30 seconds and check again.

### Symptom: "429 Too Many Requests" from webhook endpoint

The Bundle creation rate limit (100 req/min per Pipeline) has been exceeded. This typically means CI is creating Bundles faster than the controller can process them.

Reduce CI frequency or increase the rate limit via controller configuration.

## Graph controller issues

### Symptom: Graph CR created but no PromotionSteps appear

The Graph controller is not reconciling. Check:

```bash
# Is the Graph controller running?
kubectl get pods -n kro-system

# Check Graph status
kubectl get graph my-app-v1-29-0 -o yaml
```

If the Graph controller is not running, PromotionSteps will not be created. kardinal-promoter requires the Graph controller to be operational.

### Symptom: Graph shows "Accepted: False"

The Graph spec is invalid. Check the Graph status conditions for the error message:

```bash
kubectl get graph my-app-v1-29-0 -o jsonpath='{.status.conditions}'
```

Common causes:
- Invalid CEL expression in a readyWhen clause
- Circular dependency between nodes
- Reference to a non-existent node ID

## Debugging commands

```bash
# Overview of all pipelines
kardinal get pipelines

# Detailed view of steps and gates
kardinal get steps my-app

# Why is an environment blocked?
kardinal explain my-app --env prod

# Continuous watch (re-evaluates on change)
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

### Symptom: PolicyGate stays in FAIL or shows "CEL error"

```bash
# Check the gate's current status
kubectl get policygate my-gate -o yaml | grep -A10 status

# Show the expression and current evaluation
kardinal explain my-app --env prod
```

**CEL syntax error:** The expression failed to compile. Common mistakes:
- Parentheses mismatch: `!schedule.isWeekend` (correct) vs `!schedule.isWeekend()` (wrong — it's a map field, not a function)
- Unknown variable: `bundle.version` (correct) vs `bundle.spec.images[0].tag` (not in the context; see the [CEL context reference](reference/cel-context.md))
- Type mismatch: comparing string to int without casting

Test your expression before applying:
```bash
kardinal policy simulate --pipeline my-app --env prod --time "Tuesday 10am"
```

**gate.recheckInterval too long:** The gate evaluates on each ScheduleClock tick. The default cluster clock interval is 1 minute. If your gate has `recheckInterval: 10m`, it will only re-evaluate every 10 minutes. For testing, reduce to `recheckInterval: 30s`.

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

### Symptom: "git push failed: 403 Forbidden" or "remote: Permission to ... denied"

The GitHub PAT has expired or lacks the required scope.

```bash
# Check the token secret exists
kubectl get secret github-token -o yaml

# Verify token scope — must have 'repo' scope (or 'contents:write' for fine-grained tokens)
# Test the token directly:
TOKEN=$(kubectl get secret github-token -o jsonpath='{.data.token}' | base64 -d)
curl -s -H "Authorization: token $TOKEN" https://api.github.com/user | jq .login
```

To rotate the token:
```bash
kubectl create secret generic github-token \
  --from-literal=token=<new-token> \
  --dry-run=client -o yaml | kubectl apply -f -
```

A step that returns an error is retried with backoff (10s, 20s, 40s, 80s, then 2m) up to 5 times; `status.message` shows `retrying in <d> (<n>/5)`. Rotate the token within that window and the step continues. After the last retry the PromotionStep is Failed; create a new Bundle to promote again.

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
1. After 5 consecutive failures (429 or 5xx), the circuit opens and all SCM calls are blocked for a cooldown period
2. The cooldown respects `X-RateLimit-Reset` and `Retry-After` response headers when present
3. After the cooldown, one probe request is allowed (half-open state)
4. On probe success, the circuit closes and normal operation resumes

**Checking circuit state in logs:**

```bash
# Look for circuit open/close events
kubectl logs -n kardinal-system deploy/kardinal-promoter | grep "scm circuit"

# Example log when circuit is open:
# ERR scm: github scm: SCM circuit open until 2026-04-17T05:30:00Z
```

**Manual recovery if circuit stays open too long:**

```bash
# Restart the controller to reset in-memory circuit state
kubectl rollout restart deployment/kardinal-promoter -n kardinal-system
```

**Check current GitHub rate limit:**

```bash
TOKEN=$(kubectl get secret github-token -o jsonpath='{.data.token}' | base64 -d)
curl -s -H "Authorization: token $TOKEN" https://api.github.com/rate_limit | jq .rate
```

**Long-term fix:** Use a GitHub App token (higher rate limits than PAT).

### Symptom: Push succeeds but PR is not opened

Check the controller logs for the PR creation call:
```bash
kubectl logs -n kardinal-system deploy/kardinal-promoter | grep "open-pr\|pull_request" | tail -20
```

Common causes:
- The base branch does not exist in the GitOps repo (check `spec.environments[*].branch`)
- The commit SHA is empty (a previous git-commit step failed silently — check its status)
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
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter \
  --namespace kardinal-system --reuse-values
```

### Symptom: Team cannot create PolicyGates in another team's namespace

This is expected behavior. RBAC isolation prevents cross-namespace modifications:
- Org gates live in `platform-policies` — only platform admins can write there
- Team gates live in the team's own namespace

Verify the ClusterRole bindings:
```bash
kubectl get rolebinding -A | grep policygate
```

---

## kro Graph controller issues

### Symptom: Graph shows `Accepted: False` with a CEL compile error

The Graph spec contains an invalid CEL expression in a `readyWhen` or `includeWhen` clause.

```bash
# Check the Graph status
kubectl get graph -l kardinal.io/bundle=my-app-v1 -o yaml | grep -A20 conditions

# Check kro logs
kubectl logs -n kro-system deployment/kro --tail=100 | grep -i error
```

This usually means a node template contains malformed `${...}` expressions. Check the translator output by looking at the Graph spec's nodes.

### Symptom: Graph is created but reconciler does not advance (stuck in "Reconciling")

```bash
# Check Graph revision status
kubectl get graphrevisions -l kardinal.io/pipeline=my-app 2>/dev/null

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

---

## Performance tuning (large-scale deployments)

### 50+ environments / 100+ concurrent Bundles

The controller handles each Bundle independently via a dedicated Graph. For very large deployments, consider:

**1. Raise the controller's resource limits.** Extra replicas do not add throughput: the
controller runs with `--leader-elect`, so only one replica reconciles and the others are
standbys (`replicaCount`, default 1).
```yaml
# values.yaml
controller:
  resources:
    limits:
      cpu: "2"
      memory: 2Gi
    requests:
      cpu: 500m
      memory: 512Mi
```

**2. Reconcile concurrency** is controller-runtime's default of one worker per CRD type;
there is no flag to change it.

**3. Reduce ScheduleClock tick frequency** if no gate needs minute-level `schedule.*`
re-evaluation. The chart owns the `kardinal-clock` ScheduleClock, so set it through Helm:
```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter \
  -n kardinal-system --reuse-values --set scheduleClock.interval=5m
```

**4. Bundle history** — finished Bundles are garbage-collected per Pipeline. Set
`spec.historyLimit` on the Pipeline to keep fewer (see
[Pipeline reference](pipeline-reference.md#spechistorylimit)).

**5. Monitor controller performance:**
```bash
# Check reconcile queue depth (via Prometheus if PrometheusRule is installed)
kubectl port-forward svc/kardinal-promoter -n kardinal-system 8080:8080
curl -s http://localhost:8080/metrics | grep "^workqueue_depth"

# Or use the built-in Prometheus alerts
kubectl get prometheusrule kardinal-promoter -n kardinal-system -o yaml
```
