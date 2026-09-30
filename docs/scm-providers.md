# SCM Providers

kardinal-promoter supports multiple Source Control Management (SCM) providers for
pull request and merge request lifecycle operations. The provider is configured on the
controller at startup.

## Supported Providers

| Provider | `--scm-provider` value | PR type | Webhook header checked | PR labels | Approvals read |
|---|---|---|---|---|---|
| GitHub / GitHub Enterprise | `github` (default) | Pull Requests | `X-Hub-Signature-256` (HMAC-SHA256) | Yes | Yes |
| GitLab (incl. subgroups) | `gitlab` | Merge Requests | `X-Gitlab-Token` (shared token) | Yes | Yes |
| Forgejo / Codeberg | `forgejo` | Pull Requests | `X-Forgejo-Signature` or `X-Gitea-Signature` (HMAC-SHA256) | Yes | Yes |
| Gitea | `gitea` | Pull Requests | `X-Gitea-Signature` or `X-Hub-Signature-256` (HMAC-SHA256) | Yes | Yes |
| Bitbucket Cloud | `bitbucket` | Pull Requests | `X-Hub-Signature` (HMAC-SHA256) | No (Bitbucket has no PR labels) | Yes |
| Azure DevOps | `azuredevops` | Pull Requests | `X-AzureDevOps-Token` (custom header you add to the service hook) | Yes (PR tags) | Yes |

All providers send webhooks to the same endpoint, `http://<controller-host>:8083/webhook/scm`.
Webhooks only speed things up: without them, the controller still sees merges by polling.

Bitbucket Cloud and Azure DevOps are newer and less tested than GitHub and GitLab.
On Bitbucket, PRs carry no `kardinal` or `kardinal/rollback` labels, so find rollback
PRs by their `[kardinal] Rollback` title instead.

SCM webhooks are delivered to `POST /webhook/scm` on the webhook port (`8083`). Without
`--webhook-secret` the endpoint rejects every event with `401`, and merges are detected by
PR status polling instead.

---

## GitHub

### Controller flags

```bash
kardinal-controller \
  --scm-provider github \
  --github-token $GITHUB_TOKEN \
  --webhook-secret $KARDINAL_WEBHOOK_SECRET
```

Alternatively, set environment variables:

```bash
export GITHUB_TOKEN=ghp_...
export KARDINAL_WEBHOOK_SECRET=my-hmac-secret
export KARDINAL_SCM_PROVIDER=github
```

### Required token scopes

| Scope | Purpose |
|---|---|
| `repo` | Create/close pull requests, post comments, read PR status |
| `write:repo_hook` | (Optional) Register webhooks programmatically |

### Webhook configuration

1. In your GitHub repository, go to **Settings → Webhooks → Add webhook**.
2. Set **Payload URL** to `http://<controller-host>:8083/webhook/scm`.
3. Set **Content type** to `application/json`.
4. Set **Secret** to the same value as `--webhook-secret`.
5. Select **Pull request** events.

### GitHub Enterprise

Use `--scm-api-url` to override the API base URL:

```bash
kardinal-controller \
  --scm-provider github \
  --github-token $GITHUB_TOKEN \
  --scm-api-url https://github.example.com/api/v3
```

---

## GitLab

### Controller flags

```bash
kardinal-controller \
  --scm-provider gitlab \
  --github-token $GITLAB_TOKEN \
  --webhook-secret $KARDINAL_WEBHOOK_SECRET
```

> Note: `--github-token` is the SCM token for both providers. For GitLab, pass a
> **private token** (e.g., `glpat-...`) or a project access token.

Alternatively, set environment variables:

```bash
export GITHUB_TOKEN=glpat-...         # GitLab private token
export KARDINAL_WEBHOOK_SECRET=my-token
export KARDINAL_SCM_PROVIDER=gitlab
export KARDINAL_SCM_API_URL=https://gitlab.com  # or your self-managed URL
```

### Required token scopes

| Scope | Purpose |
|---|---|
| `api` | Full API access — required for MR creation, comments, and label updates |

A **project access token** with `api` scope is recommended over a personal access token
for production deployments.

### Webhook configuration

1. In your GitLab project, go to **Settings → Webhooks**.
2. Set **URL** to `http://<controller-host>:8083/webhook/scm`.
3. Set **Secret token** to the same value as `--webhook-secret`.
4. Enable **Merge request events**.
5. Click **Add webhook**.

> GitLab validates webhooks by comparing the `X-Gitlab-Token` header against the
> configured secret (plaintext comparison, not HMAC).

### Self-managed GitLab

Use `--scm-api-url` to override the API base URL:

```bash
kardinal-controller \
  --scm-provider gitlab \
  --github-token $GITLAB_TOKEN \
  --scm-api-url https://gitlab.example.com
```

---

## Forgejo / Gitea

Forgejo (including Codeberg.org) and Gitea share the same REST API v1. Use `forgejo`
for Forgejo instances and `gitea` for Gitea instances — both map to the same provider
implementation.

### Controller flags

```bash
kardinal-controller \
  --scm-provider forgejo \
  --github-token $FORGEJO_TOKEN \
  --scm-api-url https://codeberg.org \
  --webhook-secret $KARDINAL_WEBHOOK_SECRET
```

Alternatively, set environment variables:

```bash
export GITHUB_TOKEN=your-forgejo-token
export KARDINAL_WEBHOOK_SECRET=my-hmac-secret
export KARDINAL_SCM_PROVIDER=forgejo
export KARDINAL_SCM_API_URL=https://codeberg.org   # or your self-hosted Forgejo URL
```

### Required token scopes

| Scope | Purpose |
|---|---|
| `write:issue` | Post comments on pull requests |
| `write:repository` | Create and close pull requests, add labels |

Create an API token in your Forgejo/Gitea instance under **Settings → Applications → Access Tokens**.

### Webhook configuration

1. In your Forgejo/Gitea repository, go to **Settings → Webhooks → Add Webhook → Gitea**.
2. Set **Target URL** to `http://<controller-host>:8083/webhook/scm`.
3. Set **Secret** to the same value as `--webhook-secret`.
4. Select **Pull Request** events.
5. Click **Add Webhook**.

> Forgejo/Gitea signs webhooks with HMAC-SHA256 (same algorithm as GitHub). Forgejo
> sends the signature in `X-Forgejo-Signature` and `X-Gitea-Signature`; Gitea sends
> `X-Gitea-Signature`. The controller accepts any of them.

### Codeberg.org (public Forgejo instance)

Codeberg is the primary public Forgejo instance. Use `--scm-api-url https://codeberg.org`:

```bash
kardinal-controller \
  --scm-provider forgejo \
  --github-token $CODEBERG_TOKEN \
  --scm-api-url https://codeberg.org
```

---

## Bitbucket Cloud

```bash
kardinal-controller \
  --scm-provider bitbucket \
  --github-token $BITBUCKET_ACCESS_TOKEN \
  --webhook-secret $KARDINAL_WEBHOOK_SECRET
```

Use a repository, project or workspace **access token** with pull request write
access. The controller sends it as a Bearer token, so app passwords do not work.
The repository is `workspace/repo`, taken from the Pipeline's `spec.git.url`.

Webhook: in the repository go to **Repository settings → Webhooks → Add webhook**,
set the URL to `http://<controller-host>:8083/webhook/scm`, set **Secret** to the
value of `--webhook-secret`, and select the **Pull request: Merged** trigger.

---

## Azure DevOps

```bash
kardinal-controller \
  --scm-provider azuredevops \
  --github-token $ADO_PAT \
  --webhook-secret $KARDINAL_WEBHOOK_SECRET
```

Use a PAT with **Code (Read & write)**. The repository is `org/project/repo`, taken
from `spec.git.url` (`https://dev.azure.com/org/project/_git/repo`).

Webhook: create a **Web Hooks** service hook for **Pull request updated**, set the URL
to `http://<controller-host>:8083/webhook/scm`, and add the HTTP header
`X-AzureDevOps-Token: <value of --webhook-secret>`. A PR counts as merged only when its
status is `completed`.

---

## Pipeline CRD configuration

The controller uses one SCM provider for every Pipeline, chosen by `--scm-provider`
(or `KARDINAL_SCM_PROVIDER`). `spec.git.provider` is **deprecated and ignored**: it does
not select a provider, and the CRD accepts only `github` or `gitlab` there. Leave it
unset. The repository comes from `spec.git.url`.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-pipeline
spec:
  git:
    url: https://codeberg.org/myorg/myrepo
    branch: main
  environments:
    - name: dev
    - name: prod
      approval: pr-review
```

---

## Token check at startup

When a token is set, the controller checks it once at startup, in the background, and
logs what it finds. This also runs in Helm installs, where the token comes from the
watched Secret. The check never stops the controller from starting.

| Provider | Call | Logged as a warning |
|---|---|---|
| GitHub / GitHub Enterprise | `GET /user` | token rejected (401); a classic PAT without `repo` or `public_repo`; a fine-grained PAT or GitHub App token, whose permissions the call cannot show |
| GitLab | `GET /api/v4/personal_access_tokens/self` | token rejected; no `api` scope |
| Forgejo / Gitea | `GET /api/v1/user` | token rejected |
| Bitbucket Cloud, Azure DevOps | none | not checked; an info line says so, and token problems show on the first promotion step |

Find the warnings with:

```bash
kubectl logs -n kardinal-system -l app.kubernetes.io/name=kardinal-promoter \
  | grep "SCM TOKEN SCOPE WARNING"
```

A network or HTTP error from the check is logged at debug level only. The token itself is
never logged. A token loaded later by the Secret watcher (after a rotation) is not
checked.

---

## Credential rotation (zero-downtime)

kardinal-promoter supports rotating SCM credentials at runtime without restarting the controller or
causing a gap in active promotions.

### How it works

When the controller is configured to read the token from a Kubernetes Secret
(via `--scm-token-secret-name` or the Helm `github.secretRef.name` value), a background
`SecretWatcher` polls that Secret every **30 seconds**. When the token value changes, the
watcher atomically reloads the SCM provider using `sync/atomic.Pointer` semantics — concurrent
reconciler goroutines see a consistent token snapshot at all times and are never interrupted.

The three controller flags that enable this mode are also configurable via environment variables:

| Flag | Environment variable | Default |
|---|---|---|
| `--scm-token-secret-name` | `KARDINAL_SCM_TOKEN_SECRET_NAME` | (empty — static token mode) |
| `--scm-token-secret-namespace` | `KARDINAL_SCM_TOKEN_SECRET_NAMESPACE` | `POD_NAMESPACE` → `kardinal-system` |
| `--scm-token-secret-key` | `KARDINAL_SCM_TOKEN_SECRET_KEY` | `token` |

When `--scm-token-secret-name` is **not set**, the controller uses the token passed via
`--github-token` / `GITHUB_TOKEN` at startup (static mode). Rotating requires a controller
restart in static mode.

When `github.secretRef.name` or `github.token` is set in the Helm chart, these three environment
variables are injected into the controller Deployment automatically. With `github.token` the
Secret is the chart-managed `<release>-github-token`; rotate it with
`helm upgrade --reuse-values --set github.token=<NEW_TOKEN>`.

### Rotating a PAT (zero-downtime procedure)

1. Generate the new token in your SCM provider and copy it.
2. Update the Kubernetes Secret:
   ```bash
   kubectl create secret generic github-token \
     --namespace kardinal-system \
     --from-literal=token=<NEW_TOKEN> \
     --dry-run=client -o yaml | kubectl apply -f -
   ```
3. Within 30 seconds the controller picks up the change. No controller restart is needed.
   Promotions in flight are not interrupted — the atomic swap completes before the next
   reconcile iteration reads the token.
4. Verify the rotation took effect by checking the controller log:
   ```bash
   kubectl logs -n kardinal-system -l app.kubernetes.io/name=kardinal-promoter --tail=20 \
     | grep "SCM credentials rotated"
   ```

### Static mode (development / CI)

If you run the controller with `--github-token` (or only the `GITHUB_TOKEN` env var) and no
`--scm-token-secret-name`, no Secret watching is configured. To rotate the token you must
restart the controller:

```bash
kubectl rollout restart deployment/kardinal-promoter -n kardinal-system
```

---

## Adding a new SCM provider

Implement the `SCMProvider` interface in `pkg/scm/` and register it in
`pkg/scm/factory.go`:

```go
func NewProvider(providerType, token, apiURL, webhookSecret string) (SCMProvider, error) {
    switch providerType {
    case "github", "":
        return NewGitHubProvider(token, apiURL, webhookSecret), nil
    case "gitlab":
        return NewGitLabProvider(token, apiURL, webhookSecret), nil
    // Add your provider here.
    }
}
```
