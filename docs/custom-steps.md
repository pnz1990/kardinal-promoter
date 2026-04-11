# Custom Promotion Steps

Custom steps let you inject arbitrary HTTP webhook calls into the promotion
sequence. Any step `uses:` value that is not a built-in step name is dispatched
as an HTTP POST to the configured webhook URL.

## Quick start

1. Write a small HTTP server that implements the [contract](#contract) below.
2. Add the step to your Pipeline environment's `steps:` list.
3. Deploy the server to your cluster.

```yaml
# Pipeline snippet
spec:
  environments:
    - name: prod
      steps:
        - uses: my-team/version-gate          # non-built-in → custom step
          webhook:
            url: http://custom-step-server.my-ns.svc.cluster.local/step
            timeoutSeconds: 30
            secretRef:
              name: custom-step-token          # optional: K8s Secret with Authorization header
        - uses: git-clone                      # built-in steps follow
        - uses: kustomize-set-image
        - uses: git-commit
        - uses: git-push
        - uses: open-pr
        - uses: wait-for-merge
        - uses: health-check
```

## Contract

### Request

`POST <webhook.url>`

Headers:
- `Content-Type: application/json`
- `Authorization: <value from secretRef>` (only if `secretRef` is configured)

Body:

```json
{
  "bundle": {
    "type": "image",
    "images": [{"repository": "ghcr.io/myorg/app", "tag": "v2.0.0"}]
  },
  "environment": "prod",
  "inputs": {
    "webhook.url": "http://...",
    "webhook.timeoutSeconds": "30"
  },
  "outputs_so_far": {
    "branch": "kardinal/my-app-v2-0-0/prod"
  }
}
```

### Response

```json
{
  "result": "pass",
  "outputs": {"scan_id": "abc123"},
  "message": "all security checks passed"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `result` | `"pass"` \| `"fail"` | Yes | `"pass"` continues the pipeline; `"fail"` marks the PromotionStep as Failed |
| `outputs` | `map[string]string` | No | Key/value pairs merged into `PromotionStep.status.outputs` and available to subsequent steps |
| `message` | `string` | No | Human-readable explanation shown by `kardinal explain` |

### Status codes

| Status | Behaviour |
|---|---|
| 2xx | Parse response body, use `result` field |
| 4xx | Mark step Failed immediately (no retry) |
| 5xx | Retry up to 3 times with 30-second backoff, then fail |
| Timeout | If the server does not respond within `timeoutSeconds`, mark step Failed |

## Pipeline `steps:` field

When `spec.environments[*].steps` is set, it **replaces** the default step
sequence for that environment. List both custom and built-in steps in the order
you want them to execute.

If `steps:` is not set, the default sequence is used (see
[Built-in steps](#built-in-steps)).

## Built-in steps

| Step name | Description |
|---|---|
| `git-clone` | Clone the GitOps repository |
| `kustomize-set-image` | Update image tag in `kustomization.yaml` |
| `helm-set-image` | Update image tag in `values.yaml` |
| `kustomize-build` | Render manifests (layout: branch) |
| `config-merge` | Apply config-only overlay (type: config bundles) |
| `git-commit` | Commit changes to a promotion branch |
| `git-push` | Push the promotion branch |
| `open-pr` | Open a pull request |
| `wait-for-merge` | Poll until the PR is merged |
| `health-check` | Verify deployment health via the configured health adapter |

## Authentication

The webhook can be authenticated with a Kubernetes Secret containing the
`Authorization` header value.

```yaml
webhook:
  url: https://my-server/step
  secretRef:
    name: my-custom-step-secret
    namespace: default   # optional; defaults to the Pipeline namespace
```

The Secret must contain an `Authorization` key:

```bash
kubectl create secret generic my-custom-step-secret \
  --from-literal=Authorization="Bearer my-token"
```

The kardinal controller reads the Secret at execution time and injects the header
into the POST request.

## Idempotency requirement

The controller may call your webhook multiple times for the same step execution
(for example, after a controller restart). Your server **must** be idempotent:
calling it twice with the same request must produce the same result.

A common pattern: use the `bundle.provenance.commitSHA` and `environment` from
the request body as a cache key to detect and deduplicate re-runs.

## Step outputs and data flow

Outputs from a custom step are merged into `PromotionStep.status.outputs` and
passed to all subsequent steps (including built-in steps) via `outputs_so_far`.

Example: a security scan step returns `{"scan_report_url": "https://..."}`. A
subsequent built-in `open-pr` step will include that URL in the PR body if it is
present in `outputs_so_far`.

## Example server

A complete example custom step server is in `examples/custom-step/`.

```bash
# Run locally
go run examples/custom-step/server.go

# Apply the example Pipeline
kubectl apply -f examples/custom-step/pipeline.yaml
```

The example implements a version gate: pre-release image tags (containing
`alpha`, `beta`, `rc`, `snapshot`, or `dev`) are rejected in the `prod`
environment.

## Troubleshooting

### Step stays in `Running` state

Check that your server is reachable from the controller pod:

```bash
kubectl exec -n kardinal-system deploy/kardinal-controller -- \
  curl -s http://custom-step-server.my-ns.svc.cluster.local/healthz
```

### Step fails with "missing input webhook.url"

The `webhook.url` field in the Pipeline step spec is required for custom steps.
Verify your Pipeline YAML contains the `webhook:` block under the custom step.

### Viewing custom step output

```bash
kardinal explain my-pipeline --env prod
# Shows step state, message, and outputs from the custom step
```
