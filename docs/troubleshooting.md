# Troubleshooting

Common problems and how to diagnose them.

## Start here: `kardinal doctor`

Before diving into specific issues, run the pre-flight check to rule out common
configuration problems:

```bash
kardinal doctor
```

This checks: controller reachability, CRDs, krocodile, and the GitHub token secret.
If any check fails, the output includes a remediation hint.

For a specific pipeline: `kardinal doctor --pipeline my-app`

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
- `no-weekend-deploys`: it is a weekend. Wait for Monday or create a SkipPermission gate.
- `staging-soak`: the upstream environment was verified recently. Wait for the soak time to pass.
- CEL error: the expression references an attribute from a later phase. Check `kardinal policy test <file>`.

### Symptom: PromotionStep stays in "WaitingForMerge"

The PR has been opened but not merged. Check:

```bash
# Find the PR URL
kubectl get promotionstep my-app-v1-29-0-prod -o jsonpath='{.status.prURL}'
```

Common causes:
- PR needs review (CODEOWNERS, required reviewers)
- CI checks failing on the PR
- PR was accidentally closed (controller will not reopen closed PRs)

**If the PR was merged but the step is still "WaitingForMerge"**: this can happen if the controller was down when the webhook arrived. On next controller restart, startup reconciliation automatically re-checks all in-flight PRs and advances any that were merged during downtime. You can also force a restart:

```bash
kubectl rollout restart deployment/kardinal-controller -n kardinal-system
```

To verify webhook connectivity:

```bash
curl http://kardinal-controller:8083/webhook/scm/health
# Returns: {"status":"ok","webhookConfigured":true,"eventsProcessed":N}
```

`webhookConfigured: false` means the `--webhook-secret` flag is not set — GitHub will reject signature validation. Set `KARDINAL_WEBHOOK_SECRET` in your controller deployment.

### Symptom: PromotionStep stays in "HealthChecking"

The health adapter has not reported the environment as healthy.

```bash
# Check the PromotionStep status
kubectl get promotionstep my-app-v1-29-0-prod -o yaml
```

Common causes:
- Argo CD Application has not synced yet (check Application sync status)
- Deployment pods are crash-looping (check pod logs)
- Health timeout is too short for slow deploys (increase `health.timeout`)
- The health adapter is using the wrong resource name (check `health.type` and the resource config)

### Symptom: PolicyGate shows "CEL error"

The CEL expression references an attribute that does not exist in the current phase.

```bash
# Validate the expression
kardinal policy test my-gate.yaml
```

The output will show which attribute is unavailable. For example, `delegation.status` and `externalApproval.*` are planned future attributes — remove them or wait for that feature to ship. Attributes like `metrics.*` and `bundle.upstreamSoakMinutes` are available now but require the `MetricCheck` CRD to be configured for the relevant environment.

## Bundle not promoting

### Symptom: Bundle stays in "Available"

The Bundle was created but no Graph was generated. Check:

```bash
# Is there a Pipeline for this Bundle?
kubectl get pipelines
kubectl get bundle <name> -o yaml | grep kardinal.io/pipeline
```

The `kardinal.io/pipeline` label on the Bundle must match a Pipeline name. If the label is missing or mismatched, the controller ignores the Bundle.

### Symptom: Bundle status is "SkipDenied"

The Bundle's `intent.skip` lists an environment that has org-level PolicyGates, but no SkipPermission gate allows the skip.

```bash
kubectl get bundle <name> -o jsonpath='{.status.reason}'
```

Either remove `intent.skip` from the Bundle or create a SkipPermission PolicyGate in the `platform-policies` namespace.

## Git errors

### Symptom: "push failed: conflict" in controller logs

Another process (or another kardinal-controller replica) pushed to the same branch between the controller's fetch and push. The controller retries up to 3 times with re-fetch.

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

### Symptom: Remote cluster health check fails with "connection refused"

The kubeconfig Secret for the remote cluster contains invalid or expired credentials.

```bash
# Test the kubeconfig
KUBECONFIG=<(kubectl get secret prod-cluster -o jsonpath='{.data.kubeconfig}' | base64 -d) kubectl get pods
```

## Webhook issues

### Symptom: PRs merged but PromotionStep not advancing

The merge event webhook was not received. Check:

```bash
# Check controller logs for webhook events
kubectl logs -n kardinal-system deploy/kardinal-controller | grep webhook
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
- Unknown variable: `bundle.spec.images[0].tag` (correct) vs `bundle.images.tag` (wrong field path)
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
# Force re-evaluation by annotating the gate
kubectl annotate policygate no-weekend-deploys \
  kardinal.io/force-recheck=$(date +%s) --overwrite

# Or trigger a ScheduleClock tick
kubectl annotate scheduleclock kardinal-clock \
  kardinal.io/manual-tick=$(date +%s) -n kardinal-system --overwrite
```

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

The controller will automatically retry the failed step on the next reconcile (within 30 seconds).

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
kubectl logs -n kardinal-system deploy/kardinal-controller | grep "scm circuit"

# Example log when circuit is open:
# ERR scm: github scm: SCM circuit open until 2026-04-17T05:30:00Z
```

**Manual recovery if circuit stays open too long:**

```bash
# Restart the controller to reset in-memory circuit state
kubectl rollout restart deployment/kardinal-controller -n kardinal-system
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
kubectl logs -n kardinal-system deploy/kardinal-controller | grep "open-pr\|pull_request" | tail -20
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
  --as=system:serviceaccount:kardinal-system:kardinal-controller-manager

# Check for RBAC errors in logs
kubectl logs -n kardinal-system deploy/kardinal-controller | grep -i "forbidden\|permission"
```

The Helm chart installs a ClusterRole with all required permissions. If you customized RBAC or installed in a restricted namespace, re-apply the Helm chart:
```bash
helm upgrade kardinal oci://ghcr.io/pnz1990/kardinal-promoter/chart \
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

## krocodile / Graph controller issues

### Symptom: Graph shows "GraphRevision: Error" with "CEL compile error"

The Graph spec contains an invalid CEL expression in a `readyWhen` or `propagateWhen` clause.

```bash
# Check the Graph status
kubectl get graph -l kardinal.io/bundle=my-app-v1 -o yaml | grep -A20 conditions

# Check krocodile logs
kubectl logs -n kro-system -l app=kro-controller --tail=100 | grep -i error
```

This usually means a node template contains malformed `${...}` expressions. Check the translator output by looking at the Graph spec's nodes.

### Symptom: Graph is created but reconciler does not advance (stuck in "Reconciling")

```bash
# Check Graph revision status
kubectl get graphrevisions -l kardinal.io/pipeline=my-app 2>/dev/null

# Check for CRD schema issues
kubectl get crd policygates.kardinal.io -o jsonpath='{.status.conditions}' | python3 -m json.tool

# Verify krocodile is running
kubectl get pods -n kro-system
```

If krocodile is in CrashLoopBackOff:
```bash
kubectl describe pod -n kro-system -l app=kro-controller
kubectl logs -n kro-system -l app=kro-controller --previous
```

### Symptom: PromotionStep CRDs are not created even though Graph exists

The Graph controller creates PromotionSteps only when `propagateWhen` is satisfied for the preceding node. Check the Graph's node statuses:
```bash
kubectl get graph -l kardinal.io/bundle=my-app-v1 -o jsonpath='{.items[0].status.nodes}'
```

If a PolicyGate node is not ready, downstream PromotionSteps will not be created until it passes.

---

## Performance tuning (large-scale deployments)

### 50+ environments / 100+ concurrent Bundles

The controller handles each Bundle independently via a dedicated Graph. For very large deployments, consider:

**1. Increase controller replicas and resource limits:**
```yaml
# values.yaml
controller:
  replicas: 3
  resources:
    limits:
      cpu: "2"
      memory: 2Gi
    requests:
      cpu: 500m
      memory: 512Mi
```

**2. Tune reconcile concurrency** (controller-runtime default is 1 worker per CRD type):
```yaml
controller:
  extraArgs:
    - --concurrent-reconcilers=5
```

**3. Reduce ScheduleClock tick frequency** for pipelines that don't need sub-minute gate re-evaluation:
```bash
# Slow the cluster clock to 5m for non-time-sensitive pipelines
kubectl patch scheduleclock kardinal-clock -n kardinal-system \
  --type=merge -p '{"spec":{"interval":"5m"}}'
```

**4. Bundle supersession cleanup** — old Superseded Bundles accumulate. The controller retains 10 Bundles per pipeline by default. Adjust via:
```yaml
controller:
  bundleRetentionCount: 5  # retain only 5 Bundles per pipeline
```

**5. Monitor controller performance:**
```bash
# Check reconcile queue depth (via Prometheus if PrometheusRule is installed)
kubectl port-forward svc/kardinal-metrics -n kardinal-system 8080:8080
curl http://localhost:8080/metrics | grep controller_runtime_reconcile_queue_length

# Or use the built-in Prometheus alerts
kubectl get prometheusrule kardinal-alerts -n kardinal-system -o yaml
```
