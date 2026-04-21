# CI Integration

kardinal-promoter is triggered by your CI pipeline. After building and pushing a container image, CI creates a Bundle that enters the promotion pipeline.

## Bundle Creation Methods

### HTTP Webhook

The controller exposes a `/api/v1/bundles` endpoint that accepts JSON payloads.

```bash
curl -X POST https://kardinal.example.com/api/v1/bundles \
  -H "Authorization: Bearer $KARDINAL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "pipeline": "my-app",
    "type": "image",
    "images": [
      {
        "repository": "ghcr.io/myorg/my-app",
        "tag": "1.29.0",
        "digest": "sha256:a1b2c3d4e5f6..."
      }
    ],
    "provenance": {
      "commitSHA": "abc123def456",
      "ciRunURL": "https://github.com/myorg/my-app/actions/runs/12345",
      "author": "engineer-name"
    }
  }'
```

The endpoint creates a Bundle CRD in the cluster. Authentication is via Bearer token validated against a Kubernetes Secret. The token is scoped per Pipeline.

### GitHub Action

The action is at `.github/actions/create-bundle/` and uses composite steps (no Docker
container required). Authenticate via the `KARDINAL_TOKEN` environment variable.

**Single-image promotion** (most common):

```yaml
name: Build and Promote
on:
  push:
    branches: [main]

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Build and push image
        id: build
        uses: docker/build-push-action@v5
        with:
          push: true
          tags: ghcr.io/${{ github.repository }}:${{ github.sha }}

      - name: Create Bundle
        id: bundle
        uses: ./.github/actions/create-bundle
        env:
          KARDINAL_TOKEN: ${{ secrets.KARDINAL_TOKEN }}
        with:
          pipeline: my-app
          image: ghcr.io/${{ github.repository }}:${{ github.sha }}
          digest: ${{ steps.build.outputs.digest }}
          kardinal-url: https://kardinal.example.com

      - name: Log bundle URL
        run: echo "Promotion started: ${{ steps.bundle.outputs.bundle-status-url }}"
```

**Multi-image promotion** (for services with multiple containers):

```yaml
      - name: Create Bundle
        uses: ./.github/actions/create-bundle
        env:
          KARDINAL_TOKEN: ${{ secrets.KARDINAL_TOKEN }}
        with:
          pipeline: my-app
          images: |
            ghcr.io/myorg/app:${{ github.sha }}
            ghcr.io/myorg/sidecar:${{ github.sha }}
          kardinal-url: https://kardinal.example.com
```

**Action inputs:**

| Input | Required | Default | Description |
|---|---|---|---|
| `pipeline` | Yes | — | Pipeline name |
| `image` | No | — | Single image (`repo:tag` or `repo@sha256:digest`) |
| `digest` | No | — | Override digest for the `image` input |
| `images` | No | — | Newline-separated list of images (multi-image case) |
| `namespace` | No | `default` | Kubernetes namespace |
| `kardinal-url` | Yes | — | Controller base URL |
| `type` | No | `image` | Bundle type (`image`, `config`, `mixed`) |

**Action outputs:**

| Output | Description |
|---|---|
| `bundle-name` | Name of the created Bundle CRD |
| `bundle-namespace` | Namespace of the created Bundle CRD |
| `bundle-status-url` | Link to the pipeline view in the kardinal UI |

The action retries on transient failures (network errors, HTTP 5xx) up to 3 times
with exponential backoff. Permanent errors (HTTP 4xx — bad token, bad input) do not
retry. `KARDINAL_TOKEN` must be set as a secret in your repository settings.

### GitLab CI

```yaml
stages:
  - build
  - promote

build:
  stage: build
  script:
    - docker build -t $CI_REGISTRY_IMAGE:$CI_COMMIT_SHA .
    - docker push $CI_REGISTRY_IMAGE:$CI_COMMIT_SHA
  artifacts:
    reports:
      dotenv: build.env

promote:
  stage: promote
  script:
    - |
      curl -X POST https://kardinal.example.com/api/v1/bundles \
        -H "Authorization: Bearer $KARDINAL_TOKEN" \
        -H "Content-Type: application/json" \
        -d "{
          \"pipeline\": \"my-app\",
          \"artifacts\": {
            \"images\": [{
              \"name\": \"my-app\",
              \"reference\": \"$CI_REGISTRY_IMAGE:$CI_COMMIT_SHA\",
              \"digest\": \"$IMAGE_DIGEST\"
            }]
          },
          \"provenance\": {
            \"commitSHA\": \"$CI_COMMIT_SHA\",
            \"ciRunURL\": \"$CI_PIPELINE_URL\",
            \"author\": \"$GITLAB_USER_LOGIN\"
          }
        }"
```

### kubectl (declarative)

For teams that prefer a fully declarative approach, the Bundle can be created via kubectl in CI:

```yaml
# In your CI pipeline
- name: Create Bundle
  run: |
    cat <<EOF | kubectl apply -f -
    apiVersion: kardinal.io/v1alpha1
    kind: Bundle
    metadata:
      name: my-app-${GITHUB_SHA::8}-$(date +%s)
      labels:
        kardinal.io/pipeline: my-app
    spec:
      type: image
      pipeline: my-app
      images:
        - repository: ghcr.io/${{ github.repository }}
          tag: "${{ github.sha }}"
          digest: "${{ steps.build.outputs.digest }}"
      provenance:
        commitSHA: "${{ github.sha }}"
        ciRunURL: "${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}"
        author: "${{ github.actor }}"
        timestamp: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    EOF
```

This requires the CI runner to have kubectl access to the cluster and RBAC permissions to create Bundle CRDs.

## Authentication

### Webhook token

The `/api/v1/bundles` endpoint requires a Bearer token. The token is stored in a Kubernetes Secret and validated by the controller.

```bash
kubectl create secret generic kardinal-ci-token \
  --namespace=kardinal-system \
  --from-literal=token=$(openssl rand -hex 32)
```

The token is passed to the controller via the `--bundle-api-token` flag or the `KARDINAL_BUNDLE_TOKEN` environment variable. The endpoint is only activated when this flag is set.

Rate limiting: 60 requests per minute per token.

### kubectl access

When using the kubectl approach, CI needs a kubeconfig with a ServiceAccount that has permission to create Bundle CRDs. This is standard Kubernetes RBAC.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: bundle-creator
  namespace: default
rules:
  - apiGroups: ["kardinal.io"]
    resources: ["bundles"]
    verbs: ["create", "get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ci-bundle-creator
  namespace: default
subjects:
  - kind: ServiceAccount
    name: ci-runner
    namespace: default
roleRef:
  kind: Role
  name: bundle-creator
  apiGroup: rbac.authorization.k8s.io
```

## Provenance

The `provenance` field on the Bundle is optional but strongly recommended. It enables:
- PR evidence showing who built the image and from which commit
- PolicyGate expressions that reference provenance (e.g., `bundle.provenance.author != "dependabot[bot]"`)
- Audit trail linking deployments back to source changes

| Field | Description | Example |
|---|---|---|
| `commitSHA` | The Git commit that triggered the build | `abc123def456` |
| `ciRunURL` | URL of the CI run | `https://github.com/.../runs/12345` |
| `author` | Who or what triggered the build | `engineer-name`, `dependabot[bot]` |
| `timestamp` | When the image was built (ISO 8601) | `2026-04-09T10:00:00Z` |

## Multi-Image Bundles

A Bundle can contain multiple images for applications that deploy multiple containers together:

```json
{
  "pipeline": "my-app",
  "type": "image",
  "images": [
    {
      "repository": "ghcr.io/myorg/my-app-api",
      "tag": "1.29.0",
      "digest": "sha256:aaa..."
    },
    {
      "repository": "ghcr.io/myorg/my-app-worker",
      "tag": "1.29.0",
      "digest": "sha256:bbb..."
    }
  ]
}
```

The Kustomize update strategy will run `kustomize edit set-image` for each image in the Bundle.

## Config-Only Bundles

To promote configuration changes (resource limits, env vars, feature flags) without an image change, create a config Bundle:

```bash
curl -X POST https://kardinal.example.com/api/v1/bundles \
  -H "Authorization: Bearer $KARDINAL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "pipeline": "my-app",
    "type": "config",
    "configRef": {
      "gitRepo": "https://github.com/myorg/app-config",
      "commitSHA": "abc123def456"
    },
    "provenance": {
      "commitSHA": "abc123def456",
      "ciRunURL": "https://github.com/myorg/app-config/actions/runs/67890",
      "author": "platform-team"
    }
  }'
```

Config Bundles go through the same Pipeline, PolicyGates, and PR flow as image Bundles. The only difference is the update step: instead of `kustomize-set-image`, the controller uses `config-merge` to apply the referenced commit's changes.

## Bundle Intent

When creating a Bundle from CI, you can specify the promotion intent:

```json
{
  "pipeline": "my-app",
  "type": "image",
  "images": [ ... ],
  "provenance": { ... },
  "intent": {
    "targetEnvironment": "staging"
  }
}
```

- `targetEnvironment: prod` (default): promote through all environments up to and including prod
- `targetEnvironment: staging`: stop after staging (useful for testing)
- `skipEnvironments: ["staging"]`: skip staging (requires SkipPermission PolicyGate)

## Webhook Endpoint Reference

**URL:** `POST /api/v1/bundles`

**Headers:**
| Header | Required | Description |
|---|---|---|
| `Authorization` | Yes | `Bearer <token>` |
| `Content-Type` | Yes | `application/json` |
| `X-Kardinal-Signature` | No | HMAC-SHA256 signature for request body verification |

**Response codes:**
| Code | Meaning |
|---|---|
| 201 | Bundle created successfully |
| 400 | Invalid request body |
| 401 | Invalid or missing token |
| 404 | Pipeline not found |
| 409 | Bundle with same version already exists (idempotent) |
| 429 | Rate limit exceeded |
