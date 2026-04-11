# SCM Providers

kardinal-promoter supports multiple Source Control Management (SCM) providers for
pull request and merge request lifecycle operations. The provider is configured on the
controller at startup.

## Supported Providers

| Provider | `--scm-provider` value | PR type | Webhook validation |
|---|---|---|---|
| GitHub | `github` (default) | Pull Requests | HMAC-SHA256 (`X-Hub-Signature-256`) |
| GitLab | `gitlab` | Merge Requests | Token comparison (`X-Gitlab-Token`) |

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
    provider: gitlab          # Informational: "github" or "gitlab"
    repo: "mygroup/myrepo"
    branch: main
  environments:
    - name: dev
    - name: prod
      promotionPolicy: pr-review
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
