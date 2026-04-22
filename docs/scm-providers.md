# SCM Providers

kardinal-promoter supports multiple Source Control Management (SCM) providers for
pull request and merge request lifecycle operations. The provider is configured on the
controller at startup.

## Supported Providers

| Provider | `--scm-provider` value | PR type | Webhook validation |
|---|---|---|---|
| GitHub | `github` (default) | Pull Requests | HMAC-SHA256 (`X-Hub-Signature-256`) |
| GitLab | `gitlab` | Merge Requests | Token comparison (`X-Gitlab-Token`) |
| Forgejo / Codeberg | `forgejo` | Pull Requests | HMAC-SHA256 (`X-Gitea-Signature`) |
| Gitea | `gitea` | Pull Requests | HMAC-SHA256 (`X-Gitea-Signature`) |

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
2. Set **Payload URL** to `http://<controller-host>:8083/webhook`.
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
2. Set **URL** to `http://<controller-host>:8083/webhook`.
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
2. Set **Target URL** to `http://<controller-host>:8083/webhook`.
3. Set **Secret** to the same value as `--webhook-secret`.
4. Select **Pull Request** events.
5. Click **Add Webhook**.

> Forgejo/Gitea validates webhooks using HMAC-SHA256 (same algorithm as GitHub).
> The signature is sent in the `X-Gitea-Signature` header.

### Codeberg.org (public Forgejo instance)

Codeberg is the primary public Forgejo instance. Use `--scm-api-url https://codeberg.org`:

```bash
kardinal-controller \
  --scm-provider forgejo \
  --github-token $CODEBERG_TOKEN \
  --scm-api-url https://codeberg.org
```

---

## Pipeline CRD configuration

Set `spec.git.provider` in your Pipeline to document which SCM is used for that pipeline.
The controller reads the provider from the startup flag, not from the Pipeline CRD.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-pipeline
spec:
  git:
    provider: forgejo          # Informational: "github", "gitlab", "forgejo", or "gitea"
    repo: "myorg/myrepo"
    branch: main
  environments:
    - name: dev
    - name: prod
      promotionPolicy: pr-review
```

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

When `github.secretRef.name` is set in the Helm chart, these three environment variables are
injected into the controller Deployment automatically.

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

If you install with `--set github.token=<token>` (Helm) or `--github-token` (controller flag),
no Secret watching is configured. To rotate the token you must restart the controller:

```bash
kubectl rollout restart deployment/kardinal-promoter-controller -n kardinal-system
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
