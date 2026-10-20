# CI Integration

kardinal-promoter is triggered by your CI pipeline. After building and pushing a container image, CI creates a version that enters the promotion pipeline.

## Ways to create a version

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

`type` may be omitted: it is inferred from the fields, `chart` when `chart` is set, `mixed` for
`images` and `configRef`, `config` for `configRef` alone, else `image`. A `type` you set is kept
and checked against the fields.

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
        uses: pnz1990/kardinal-promoter/.github/actions/create-bundle@v0.12.1
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
        uses: pnz1990/kardinal-promoter/.github/actions/create-bundle@v0.12.1
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
| `digest` | No | — | Digest (`sha256:...`) of the `image` input. It is sent with the tag from `image` (the version records both) and replaces a digest given in `image` |
| `images` | No | — | Newline-separated list of images (multi-image case) |
| `namespace` | No | `default` | Kubernetes namespace. The action always sends it, so set it when the controller runs with `--watch-namespace`, or the API answers `403` |
| `kardinal-url` | Yes | — | Base URL of the Bundle API (the controller's webhook listener, `:8083` by default) |
| `ui-url` | No | — | Base URL of the kardinal UI (`:8082` by default). Sets `bundle-status-url`; without it that output is empty |
| `type` | No | empty | Version type (`image`, `config`, `mixed`). Empty leaves it out of the request, so the controller infers it: `mixed` for images and `config-commit`, `config` for `config-commit` alone, else `image` |
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

Creating a version is not idempotent, so the action retries only when the request
cannot have reached the controller: DNS or connection failures, and HTTP 502/503 from
a proxy. It makes up to 3 attempts with exponential backoff. Every other error —
HTTP 4xx (bad token, bad input), HTTP 500, or a timeout — fails the step without a
retry, because the version may already exist; check `kubectl get bundles` before
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
`image` or `mixed` version needs at least one `--image`, a `config` or `mixed` version needs
`--config-commit`, and `--ci-run-url` must pass the [Provenance](#provenance) check. Nothing is
created when a check fails.

```bash
kardinal create bundle my-app -n my-team \
  --image "ghcr.io/myorg/my-app:${GITHUB_SHA}" \
  --commit "${GITHUB_SHA}" \
  --commit-time "$(git show -s --format=%cI "${GITHUB_SHA}")" \
  --author "${GITHUB_ACTOR}" \
  --ci-run-url "${GITHUB_SERVER_URL}/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID}"

# A config Bundle: --config-repo defaults to the Pipeline's git.url.
kardinal create bundle my-app --type config \
  --config-repo https://github.com/myorg/app-config --config-commit "${CONFIG_SHA}"
```

`--commit`, `--author` and `--ci-run-url` set `spec.provenance`. Since v0.12.0 the
API server refuses an author over 256 characters or a CI run URL over 2048. See
[`kardinal create bundle`](reference/cli/kardinal-create-bundle.md) for every flag.

#### Wait for the outcome in CI

[`kardinal ship`](reference/cli/kardinal-ship.md) creates the same version as `kardinal create
bundle`, with the same flags. With `--wait` it follows the version and exits with a code a CI job
can act on:

```bash
kardinal ship my-app --image "ghcr.io/myorg/my-app:${GITHUB_SHA}" --wait --timeout 1h
```

| Exit code | Result | When |
|---|---|---|
| 0 | Verified | live in every environment it promotes to |
| 1 | Failed, Error | the promotion failed, the version could not be read three times in a row, or the command itself failed |
| 3 | Held | a person is needed and nothing else of it is moving: an approval gate, a hold, a paused pipeline, or a gate that does not pass and does not open by itself |
| 4 | WaitingForMerge | its pull request waits for a person to merge it, and nothing else of it is moving |
| 5 | Superseded, Rejected, Deleted | a newer version replaced it, it was rejected, or it was deleted |
| 6 | Timeout, Interrupted | `--timeout` (default 30m) passed, or Ctrl-C or SIGTERM stopped the wait; the result is printed first |

The codes are stable: a new outcome gets a new code. A stop at a gate, a hold or a pull request
counts once two reads agree, and a gate that has not been evaluated yet never stops the wait.

On stderr it prints each new state with the time since the start, the URL of each PR it opens,
and a summary at the end: per environment, how long its own promotion took (`took`, from its
first step's start, else from its upstream going live), or how long it has waited in its state
(`for`); stdout is the last line only:

```
Shipped 1.5.0 as my-app-x7k2p; following it (Ctrl-C stops following, the promotion goes on)
      +0s  test: Promoting
    +1m5s  test: Live
    +1m5s  prod: Promoting
    +2m0s  prod: Waiting for PR merge
    +2m0s  prod: PR https://github.com/myorg/gitops/pull/42
Summary after 2m4s:
  test   Live                   took 1m5s
  prod   Waiting for PR merge   for 4s
Version 1.5.0 (my-app-x7k2p) waits for its PR to be merged in prod: https://github.com/myorg/gitops/pull/42
```

`--quiet` (`-q`) prints only that last line (or, with `-o json`, the JSON document).

A gate that does not pass ends the wait with exit `3` only when it needs a person. A gate that
opens by itself keeps the job waiting, until `--timeout`: one whose expression, outside its string
literals, reads one of these [PolicyGate variables](policy-gates.md) that change with time, with a
`.` or `[` after it (`schedule.hour`, `schedule["hour"]`):

| Variable | What it covers |
|---|---|
| `schedule` | `schedule.isWeekend`, `schedule.hour`, `schedule.dayOfWeek` |
| `upstream` | `upstream.<env>.soakMinutes` and every other field of `upstream.<env>` |
| `bundle.upstreamSoakMinutes` | the soak of the environment before this one |
| `metrics` | `metrics.<name>.value`, `metrics.<name>.result` |
| `changewindow` | `changewindow.<name>` |

A name inside a string (`author != "metrics-bot"`) or as a field of something else
(`bundle.labels.soakMinutes`) does not count. An expression that reads one of them and also
something else (`!schedule.isWeekend && bundle.labels.approved == "yes"`) waits, since it may
open by itself.

So a job can wait through a soak or a deploy window. Any other gate that does not pass (on a
version's labels or its provenance) exits 3, as do an approval gate, a hold and a
paused pipeline. A pull request the SCM merges by itself (`pr.merge.auto`) is waited for while
its auto-merge has not failed. `--stop-at-gate` exits 3 at any gate that does not pass, also one
that would open by itself.

Ctrl-C stops following; the promotion carries on. `kardinal status my-app -w` follows it again.

While it waits, `ship` prints each environment's new state on stderr; at the end it prints one
line on stdout, or with `-o json` an object with `bundle`, `pipeline`, `namespace`, `type`,
`version`, `url`, `result`, `exitCode`, `message` and `environments`:

```bash
result=$(kardinal ship my-app --image "$IMAGE" --wait -o json | jq -r .result)
```

A job that ships to test and leaves prod to people can treat 3 and 4 as success: the version is
where it should be, waiting for a gate or a review.

### kubectl (declarative)

For teams that prefer a fully declarative approach, the version can be created via kubectl in CI:

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

A version created this way is not checked when it is created. A version that has nothing its type
promotes (an `image` or `mixed` version with no `images`, a `config` or `mixed` version with no
`configRef.commitSHA`) goes to phase `Failed` with condition `InvalidSpec` before any environment
is promoted.

## Authentication

### Webhook token

The `/api/v1/bundles` endpoint requires a Bearer token. The controller compares it, in
constant time, with the value of the `--bundle-api-token` flag or the `KARDINAL_BUNDLE_TOKEN`
environment variable. The endpoint is only mounted when the token or
[TokenReview](#kubernetes-tokens-tokenreview) is set; without either `/api/v1/bundles` returns `404`.

Keep the token in a Kubernetes Secret and expose it to the controller as
`KARDINAL_BUNDLE_TOKEN`. With the Helm chart, point `bundleAPI.tokenSecretRef.name` at the
Secret (the key defaults to `token`):

```bash
kubectl create secret generic kardinal-ci-token \
  --namespace=kardinal-system \
  --from-literal=token=$(openssl rand -hex 32)

helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.1 \
  -n kardinal-system --reset-then-reuse-values \
  --set bundleAPI.tokenSecretRef.name=kardinal-ci-token
```

There is one token per controller. Anyone holding it can create a version for any Pipeline
in any namespace the controller watches, so treat it like a deploy credential. With
`--watch-namespace` set, versions can only be created in that namespace (`403` otherwise).

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
`kardinal-promoter`; a person's tokens for the API server's own audience only with
`bundleAPI.acceptAPIServerAudience=true`, and a ServiceAccount's never, see
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
that sends more than 60 new tokens a minute gets `429` before any `TokenReview` is sent. The version records the caller as its
verified creator, `kardinal.io/created-by` (which an approval gate's `excludeAuthor` reads), and
in `kardinal.io/requested-by`; with the static token the creator is `bundle-api`. Each caller has its own 60-a-minute window. The static token, when
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
- Gate expressions that reference provenance (e.g., `bundle.provenance.author != "dependabot[bot]"`)
- Audit trail linking deployments back to source changes
- Lead time from the commit: with `commitTime` (`kardinal create bundle --commit-time "$(git show -s --format=%cI)"`), `commitToProductionMinutes` and the Pipeline's p50/p90 lead time include the CI build and test time

| Field | Description | Example |
|---|---|---|
| `commitSHA` | The Git commit that triggered the build | `abc123def456` |
| `ciRunURL` | URL of the CI run: an absolute `http://` or `https://` URL | `https://github.com/.../runs/12345` |
| `author` | Who or what triggered the build | `engineer-name`, `dependabot[bot]` |
| `timestamp` | When the image was built (ISO 8601) | `2026-04-09T10:00:00Z` |
| `commitTime` | When the commit was made (RFC 3339); the lead time metrics start at it, else at the version's creation, which leaves out the build. The Bundle API and `kardinal create bundle` refuse a zero time, one more than 5 minutes in the future, and one more than a year old; a commit time after the version's creation or more than a year before it (set with kubectl) is ignored | `2026-04-09T09:52:13Z` |

The PR body and the UI link `ciRunURL`, and a [promotion receipt](receipts.md) signs it, so it is
checked when a version is created. It must be empty or an absolute `http://` or `https://` URL
with a host, without user info (an `@` anywhere before the first `/` after `://`), without spaces
or control characters, and without a credential query or fragment parameter (from v0.13.0, unreleased:
v0.12.0 does not check parameters). A parameter is a
credential when its name is normalized (percent-decoded, a trailing array index such as `[]` or
`[0]` removed, lower-cased, `-` and `_` removed) and then either:

- is `sig`, `auth`, `authorization`, `jwt`, `code`, `pass`, `pwd` or `sid`; or
- ends in `token`, `secret`, `signature`, `password`, `passwd`, `passphrase`, `credential`,
  `credentials`, `key`, `accesskeyid`, `session`, `sessionid`, `assertion`, `ticket` or `bearer`,
  and is not one of the harmless names that end in `key`: `monkey`, `donkey`, `turkey`, `jockey`,
  `hockey`, `whiskey`, `hotkey`, `sortkey`, `primarykey`, `foreignkey`, `partitionkey`,
  `groupkey`, `cachekey`, `lookupkey`, and the CI identifiers `buildKey`, `planKey`, `jobKey`,
  `resultKey`, `buildResultKey`, `chainKey`, `stageKey` (Bamboo), `projectKey` and `issueKey`
  (SonarQube, Jira) and `branchKey`.

The run URLs of GitHub Actions, GitLab, Jenkins, CircleCI, Bamboo, TeamCity, Azure Pipelines,
Buildkite, Tekton, Google Cloud Build, Argo Workflows and Drone pass (each has a test vector).

So `circle-token`, `job_token`, `X-Goog-Signature`, `AWSAccessKeyId`, `subscription-key`,
`;jsessionid`, `client_assertion` and `token[]` are refused. `tokens`, `keyboard` and `sort_key`
are not, because a name only counts when it ends in the word.

- The [Bundle API](#webhook-endpoint-reference) returns `400` for any other value, and
  `kardinal create bundle --ci-run-url` returns an error.
- The Bundle CRD applies the same rule to a version created directly (`kubectl apply`, your own
  client). It cannot decode a parameter name, so it refuses any percent-encoded name instead. A
  version stored before the rule keeps its URL while the URL is unchanged.
- The other checks do not apply to a version created directly or to an existing one. Where such
  a version's `ciRunURL` fails the check, the PR body and the UI show `—` instead of a link, and
  promote and rollback (CLI, UI and automatic) leave it out of the version they create.

kardinal records provenance as the caller gives it, whichever way the version is created (version
API, `kardinal create bundle`, kubectl): it does not check `commitSHA`, `author` or `ciRunURL`
against your source repository or CI. A gate on `bundle.provenance.author` therefore trusts
everyone who holds the Bundle API token or may create Bundles in the namespace.

## Versions with several images

A version can contain multiple images for applications that deploy multiple containers together:

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

The kustomize update strategy writes an `images` entry for each image in the version, as
`kustomize edit set image` does. It edits the file in Go, keeps comments, and does not need
the `kustomize` binary.

## Config-only versions

To promote configuration changes (resource limits, env vars, feature flags) without an image change, create a config version:

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

Config versions go through the same Pipeline, gates, and PR flow as image versions. The only difference is the update step: instead of `kustomize-set-image`, the controller uses `config-merge`. It checks out `configRef.commitSHA` of `configRef.gitRepo` (default: the Pipeline repo) and copies the files under the environment's directory in that commit (`environments/<name>` or `environments[].path`) over the same directory of the GitOps checkout. Only that directory is copied; `.git`, symlinks and files outside it are not. Files deleted in the config commit are not deleted from the GitOps repo. The Pipeline's git token is sent to `configRef.gitRepo` only when it has the same scheme, host and port as the Pipeline repo, so it is never sent over plain `http://` or to another port. A config commit that has no directory for the environment fails the step.

A `mixed` version carries both `images` and `configRef`. Each environment runs `config-merge` and then the image update step, and commits both changes together. The images are written after the merge, so they win over any image pin in the config commit.

## How far a version goes

When creating a version from CI, you can specify the promotion intent:

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
  skip-permission gate in an org policy namespace must allow the skip; see
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
| `type` | No | `image`, `config`, `mixed` or `chart`. When omitted it is inferred: `chart` when `chart` is set, `mixed` for `images` and `configRef`, `config` for `configRef` alone, else `image` |
| `namespace` | No | Target namespace. Defaults to `--watch-namespace`, or `default` |
| `images` | For `image` and `mixed` | At least one image |
| `configRef` | For `config` and `mixed` | `gitRepo` and `commitSHA` (`commitSHA` is required). An `image` version with a `configRef` gets 400: it would deploy only its images |
| `chart` | For `chart` | `name` and `version` (required), `repoURL`, `digest`; promoted with `update.strategy: helm` (see [Promoting a chart version](subscription.md#promoting-a-chart-version)). A `chart` version with a `configRef` gets 400: the configRef would be dropped |
| `provenance` | No | `commitSHA`, `ciRunURL` (empty or an absolute `http(s)` URL, see [Provenance](#provenance)), `author`, `timestamp` (set to now if empty) |
| `intent` | No | `targetEnvironment`, `skipEnvironments` |

The body is limited to 1 MiB.

**Response codes:**
| Code | Meaning |
|---|---|
| 201 | Version created. The body is `{"name": "...", "namespace": "..."}` |
| 400 | Invalid request body, unknown field, or rejected by Bundle validation |
| 401 | Invalid or missing token |
| 403 | Namespace is not the one set by `--watch-namespace`, or RBAC denies a [TokenReview](#kubernetes-tokens-tokenreview) caller |
| 404 | Pipeline not found in the target namespace, or the endpoint is not enabled |
| 405 | Method other than `POST` |
| 409 | A version with the generated name already exists |
| 429 | Rate limit exceeded |
| 500 | The controller could not read the Pipeline or create the version |
| 503 | With [TokenReview](#kubernetes-tokens-tokenreview): the review API could not be reached |
