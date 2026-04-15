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
