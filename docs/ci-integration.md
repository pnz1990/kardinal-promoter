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

The endpoint creates a Bundle CRD in the cluster. Authentication is a Bearer token compared
with the controller's `--bundle-api-token` (one token for the whole controller, not one per
Pipeline). The Pipeline named in the request must exist in the target namespace.

### GitHub Action

The action is in the kardinal-promoter repository at `.github/actions/create-bundle/`.
Reference it as `pnz1990/kardinal-promoter/.github/actions/create-bundle@<tag>`. It uses
composite steps and needs `bash`, `curl` and `python3` on the runner. GitHub-hosted runners
have them. Authenticate via the `KARDINAL_TOKEN` environment variable.

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
        uses: pnz1990/kardinal-promoter/.github/actions/create-bundle@v0.9.0
        env:
          KARDINAL_TOKEN: ${{ secrets.KARDINAL_TOKEN }}
        with:
          pipeline: my-app
          image: ghcr.io/${{ github.repository }}:${{ github.sha }}
          digest: ${{ steps.build.outputs.digest }}
          kardinal-url: https://kardinal.example.com
          ui-url: https://kardinal-ui.example.com

      - name: Log bundle URL
        run: echo "Promotion started: ${{ steps.bundle.outputs.bundle-status-url }}"
```

**Multi-image promotion** (for services with multiple containers):

```yaml
      - name: Create Bundle
        uses: pnz1990/kardinal-promoter/.github/actions/create-bundle@v0.9.0
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
| `image` | No | — | Single image (`repo:tag` or `repo@sha256:digest`). If both `image` and `images` are set, `image` wins |
| `digest` | No | — | Digest (`sha256:...`) of the `image` input. It is sent with the tag from `image` (the Bundle records both) and replaces a digest given in `image` |
| `images` | No | — | Newline-separated list of images (multi-image case) |
| `namespace` | No | `default` | Kubernetes namespace. The action always sends it, so set it when the controller runs with `--watch-namespace`, or the API answers `403` |
| `kardinal-url` | Yes | — | Base URL of the Bundle API (the controller's webhook listener, `:8083` by default) |
| `ui-url` | No | — | Base URL of the kardinal UI (`:8082` by default). Sets `bundle-status-url`; without it that output is empty |
| `type` | No | `image` | Bundle type (`image`, `config`, `mixed`) |
| `config-commit` | For `config` and `mixed` | — | Commit SHA of the configuration change (`configRef.commitSHA`) |
| `config-repo` | No | the Pipeline's repository | Git repository of `config-commit` (`configRef.gitRepo`) |

**Action outputs:**

| Output | Description |
|---|---|
| `bundle-name` | Name of the created Bundle CRD |
| `bundle-namespace` | Namespace of the created Bundle CRD |
| `bundle-status-url` | Link to the pipeline view in the kardinal UI (`<ui-url>/ui/#pipeline=<pipeline>`); empty when `ui-url` is not set |

The action fills `provenance` from the job: `commitSHA` is `GITHUB_SHA`, `ciRunURL` is the
run URL, and `author` is `GITHUB_ACTOR`.

Creating a Bundle is not idempotent, so the action retries only when the request
cannot have reached the controller: DNS or connection failures, and HTTP 502/503 from
a proxy. It makes up to 3 attempts with exponential backoff. Every other error —
HTTP 4xx (bad token, bad input), HTTP 500, or a timeout — fails the step without a
retry, because the Bundle may already exist; check `kubectl get bundles` before
re-running the job. `pipeline` and `namespace` must be valid Kubernetes names.
Inputs are passed to the action's script as environment variables, so branch names
or tags used in inputs cannot inject shell commands. `KARDINAL_TOKEN` must be set as
a secret in your repository settings; the action sends it in a header file, not on
the curl command line.

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
    - echo "IMAGE_DIGEST=$(docker inspect --format='{{index .RepoDigests 0}}' $CI_REGISTRY_IMAGE:$CI_COMMIT_SHA | cut -d@ -f2)" >> build.env
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
          \"type\": \"image\",
          \"images\": [{
            \"repository\": \"$CI_REGISTRY_IMAGE\",
            \"tag\": \"$CI_COMMIT_SHA\",
            \"digest\": \"$IMAGE_DIGEST\"
          }],
          \"provenance\": {
            \"commitSHA\": \"$CI_COMMIT_SHA\",
            \"ciRunURL\": \"$CI_PIPELINE_URL\",
            \"author\": \"$GITLAB_USER_LOGIN\"
          }
        }"
```

### kardinal CLI

`kardinal create bundle` uses the kubeconfig of the CI runner, like kubectl, and applies the
same checks as the [Bundle API](#webhook-endpoint-reference): the Pipeline must exist, an
`image` or `mixed` Bundle needs at least one `--image`, a `config` or `mixed` Bundle needs
`--config-commit`, and `--ci-run-url` must pass the [Provenance](#provenance) check. Nothing is
created when a check fails.

```bash
kardinal create bundle my-app -n my-team \
  --image "ghcr.io/myorg/my-app:${GITHUB_SHA}" \
  --commit "${GITHUB_SHA}" \
  --author "${GITHUB_ACTOR}" \
  --ci-run-url "${GITHUB_SERVER_URL}/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID}"

# A config Bundle: --config-repo defaults to the Pipeline's git.url.
kardinal create bundle my-app --type config \
  --config-repo https://github.com/myorg/app-config --config-commit "${CONFIG_SHA}"
```

`--commit`, `--author` and `--ci-run-url` set `spec.provenance`. See
[`kardinal create bundle`](reference/cli/kardinal-create-bundle.md) for every flag.

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

A Bundle created this way is not checked when it is created. A Bundle that has nothing its type
promotes (an `image` or `mixed` Bundle with no `images`, a `config` or `mixed` Bundle with no
`configRef.commitSHA`) goes to phase `Failed` with condition `InvalidSpec` before any environment
is promoted.

## Authentication

### Webhook token

The `/api/v1/bundles` endpoint requires a Bearer token. The controller compares it, in
constant time, with the value of the `--bundle-api-token` flag or the `KARDINAL_BUNDLE_TOKEN`
environment variable. The endpoint is only mounted when the token is set; without it
`/api/v1/bundles` returns `404`.

Keep the token in a Kubernetes Secret and expose it to the controller as
`KARDINAL_BUNDLE_TOKEN`. With the Helm chart, point `bundleAPI.tokenSecretRef.name` at the
Secret (the key defaults to `token`):

```bash
kubectl create secret generic kardinal-ci-token \
  --namespace=kardinal-system \
  --from-literal=token=$(openssl rand -hex 32)

helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  -n kardinal-system --reset-then-reuse-values \
  --set bundleAPI.tokenSecretRef.name=kardinal-ci-token
```

There is one token per controller. Anyone holding it can create a Bundle for any Pipeline
in any namespace the controller watches, so treat it like a deploy credential. With
`--watch-namespace` set, Bundles can only be created in that namespace (`403` otherwise).

Rate limiting: 60 requests per minute. There is one token, so all callers share the limit.
Every request with the right token counts, including ones rejected with `400`; a request
over the limit gets `429`. The window is a fixed minute kept in the controller process, and
the limit cannot be changed.

The static token is a **single-tenant admin credential**. For several teams, give each CI
system its own Kubernetes identity instead:

### Kubernetes tokens (TokenReview)

With `bundleAPI.tokenReview=true` (`--bundle-api-tokenreview-auth`), `POST /api/v1/bundles`
also accepts a Kubernetes token for kardinal's audience, for example a ServiceAccount token
from `kubectl create token ci -n team-a --duration=1h --audience kardinal-promoter` or a
projected token with `audience: kardinal-promoter` in a CI pod. The controller authenticates it
with a `TokenReview` for the audiences in `tokenReview.audiences` (default
`kardinal-promoter`; tokens for the API server's own audience only with
`tokenReview.acceptAPIServerAudience=true`, see
[Option 2](guides/security.md#option-2-kubernetes-tokens-tokenreview)) and checks with a `SubjectAccessReview` that
the caller may `get` the Pipeline and `create` bundles in the target namespace, the
[promoter role](guides/security.md#user-roles):

```bash
kubectl create rolebinding ci-promoter -n team-a \
  --clusterrole=kardinal-promoter-promoter --serviceaccount=team-a:ci
TOKEN=$(kubectl create token ci -n team-a --duration=1h --audience kardinal-promoter)
curl -X POST https://kardinal.example.com/api/v1/bundles -H "Authorization: Bearer $TOKEN" \
  -d '{"pipeline":"app","namespace":"team-a","type":"image","images":[{"repository":"ghcr.io/org/app","tag":"1.2.3"}]}'
```

An invalid token, or one for another audience, gets `401`, a caller RBAC denies `403`
naming the verb, resource and namespace, and an unreachable review API `503`. A client address
that sends more than 60 new tokens a minute gets `429` before any `TokenReview` is sent. The Bundle records the caller in
`kardinal.io/requested-by`. Each caller has its own 60-a-minute window. The static token, when
set, keeps working beside it (a request whose token is the static token acts as the
controller). The chart grants the controller `create` on `tokenreviews` and
`subjectaccessreviews` when this is on.

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
| `ciRunURL` | URL of the CI run: an absolute `http://` or `https://` URL | `https://github.com/.../runs/12345` |
| `author` | Who or what triggered the build | `engineer-name`, `dependabot[bot]` |
| `timestamp` | When the image was built (ISO 8601) | `2026-04-09T10:00:00Z` |

The PR body and the UI link `ciRunURL`, so it is checked when a Bundle is created. It must be
empty or an absolute `http://` or `https://` URL with a host, without user info
(`https://user@host/...`), spaces or control characters.

- The [Bundle API](#webhook-endpoint-reference) returns `400` for any other value, and
  `kardinal create bundle --ci-run-url` returns an error.
- Bundles created directly (`kubectl apply`, your own client) and existing Bundles are not
  checked. Where such a Bundle's `ciRunURL` fails the check, the PR body and the UI show `—`
  instead of a link, and promote and rollback (CLI, UI and automatic) leave it out of the
  Bundle they create.

kardinal records provenance as the caller gives it, whichever way the Bundle is created (Bundle
API, `kardinal create bundle`, kubectl): it does not check `commitSHA`, `author` or `ciRunURL`
against your source repository or CI. A PolicyGate on `bundle.provenance.author` therefore trusts
everyone who holds the Bundle API token or may create Bundles in the namespace.

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

The kustomize update strategy writes an `images` entry for each image in the Bundle, as
`kustomize edit set image` does. It edits the file in Go, keeps comments, and does not need
the `kustomize` binary.

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

Config Bundles go through the same Pipeline, PolicyGates, and PR flow as image Bundles. The only difference is the update step: instead of `kustomize-set-image`, the controller uses `config-merge`. It checks out `configRef.commitSHA` of `configRef.gitRepo` (default: the Pipeline repo) and copies the files under the environment's directory in that commit (`environments/<name>` or `environments[].path`) over the same directory of the GitOps checkout. Only that directory is copied; `.git`, symlinks and files outside it are not. Files deleted in the config commit are not deleted from the GitOps repo. The Pipeline's git token is sent to `configRef.gitRepo` only when it has the same scheme, host and port as the Pipeline repo, so it is never sent over plain `http://` or to another port. A config commit that has no directory for the environment fails the step.

A `mixed` Bundle carries both `images` and `configRef`. Each environment runs `config-merge` and then the image update step, and commits both changes together. The images are written after the merge, so they win over any image pin in the config commit.

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

- `targetEnvironment` unset (default): promote through every environment in the Pipeline
- `targetEnvironment: staging`: stop after staging (useful for testing)
- `skipEnvironments: ["staging"]`: skip staging. If an org gate applies to staging, a
  skip-permission PolicyGate in an org policy namespace must allow the skip; see
  [Skip Permissions](policy-gates.md#skip-permissions)

`intent` and `configRef` are copied to the Bundle spec as sent. Unknown fields are
rejected with `400`, so a misspelt key fails the request instead of being ignored.

## Webhook Endpoint Reference

**URL:** `POST /api/v1/bundles`

**Headers:**
| Header | Required | Description |
|---|---|---|
| `Authorization` | Yes | `Bearer <token>` |
| `Content-Type` | No | `application/json`. Send it; the controller does not check it |

**Body fields:**
| Field | Required | Description |
|---|---|---|
| `pipeline` | Yes | Pipeline name (a valid Kubernetes name, at most 63 characters) |
| `type` | No | `image` (default), `config` or `mixed` |
| `namespace` | No | Target namespace. Defaults to `--watch-namespace`, or `default` |
| `images` | For `image` and `mixed` | At least one image |
| `configRef` | For `config` and `mixed` | `gitRepo` and `commitSHA` (`commitSHA` is required) |
| `provenance` | No | `commitSHA`, `ciRunURL` (empty or an absolute `http(s)` URL, see [Provenance](#provenance)), `author`, `timestamp` (set to now if empty) |
| `intent` | No | `targetEnvironment`, `skipEnvironments` |

The body is limited to 1 MiB.

**Response codes:**
| Code | Meaning |
|---|---|
| 201 | Bundle created. The body is `{"name": "...", "namespace": "..."}` |
| 400 | Invalid request body, unknown field, or rejected by Bundle validation |
| 401 | Invalid or missing token |
| 403 | Namespace is not the one set by `--watch-namespace` |
| 404 | Pipeline not found in the target namespace, or the endpoint is not enabled |
| 405 | Method other than `POST` |
| 409 | A Bundle with the generated name already exists |
| 429 | Rate limit exceeded |
| 500 | The controller could not read the Pipeline or create the Bundle |
