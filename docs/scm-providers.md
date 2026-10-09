# SCM Providers

kardinal-promoter supports multiple Source Control Management (SCM) providers for
pull request and merge request lifecycle operations. The provider is configured on the
controller at startup.

With the Helm chart, set `scm.provider` and `scm.apiURL`. Put the token in a Secret named by
`github.secretRef.name`, and the webhook secret in one named by `webhook.secretRef.name` (key
`secret`). The chart passes these to the controller as the flags and environment variables
below.

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
Only a merged pull request (merge request) event moves a promotion. The controller reads
the event type from `X-GitHub-Event`, `X-Forgejo-Event` or `X-Gitea-Event`, or from
GitLab's `object_kind` (`X-Gitlab-Event` when that is missing), and logs and ignores
other events such as pushes and comments. A merge event is checked before it counts: the
controller asks the SCM API once, with its token, whether the PR is merged. A merge event
for a PR the API reports not merged, or one the controller cannot check because the API
call failed, is answered with `204` and changes nothing; polling records the merge when
there is one.

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
| `repo` | Create/close pull requests, post comments, read PR status, delete the head branch of a PR kardinal closed, or of a step that ended before it opened a PR |

These are classic token scopes. With a fine-grained personal access token or a GitHub App
token, deleting the head branch is a git refs call and needs the **Contents: read and write**
repository permission, besides the pull request permissions kardinal already needs; without it
every PR kardinal closes ends with its step asking you to delete the branch by hand.

### Webhook configuration

1. In your GitHub repository, go to **Settings → Webhooks → Add webhook**.
2. Set **Payload URL** to `http://<controller-host>:8083/webhook/scm`.
3. Set **Content type** to `application/json`.
4. Set **Secret** to the same value as `--webhook-secret`.
5. Select **Pull request** events.

### GitHub App

Instead of a personal access token, kardinal can authenticate as a GitHub App installation:
the controller signs a JWT with the App's private key, exchanges it for an installation token
(`POST /app/installations/<id>/access_tokens`), caches the token, and replaces it 10 minutes
before it expires (installation tokens last one hour). PRs are then opened by the App
(`<app-name>[bot]`).

1. Create a GitHub App (**Settings → Developer settings → GitHub Apps → New GitHub App**) with
   the repository permissions **Contents: Read and write**, **Pull requests: Read and write**
   and **Metadata: Read-only**. Webhooks of the App are not used; configure the repository
   webhook below if you want them.
2. Install it on the account or organisation that owns the GitOps repositories, and note the
   installation ID (the number at the end of the installation's settings URL).
3. Generate a private key (PEM) and put the three values in the controller's Secret:

```bash
kubectl create secret generic github-app -n kardinal-system \
  --from-literal=githubAppID=123456 \
  --from-literal=githubAppInstallationID=78901234 \
  --from-file=githubAppPrivateKey=my-app.private-key.pem
helm upgrade --install kardinal-promoter ... \
  --set github.secretRef.name=github-app --set github.app.enabled=true
```

The Secret is watched like a token Secret ([Credential rotation](#credential-rotation-zero-downtime)):
a new key or ID is picked up within 30 seconds, and the controller mints a token at once to
check it, logging `SCM GitHub App installation token minted`, or `SCM GITHUB APP WARNING` with
GitHub's answer. After a failed mint kardinal waits before it asks again (5s, doubling to 5
minutes) and keeps using a token that is still valid; a token issued for less than 20 minutes is
replaced a tenth of its life before it expires. A Secret with `githubAppPrivateKey` is read as App credentials even if it also
has a `token`. Without the chart, run the controller with `--scm-token-secret-name github-app`,
or in static mode with `--github-app-id`, `--github-app-installation-id` and
`--github-app-private-key-file` (or `GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID`,
`GITHUB_APP_PRIVATE_KEY_FILE`) instead of `--github-token`.

The token is minted at `--scm-api-url`, so GitHub Enterprise Server works the same way
(`--scm-api-url https://github.example.com/api/v3`); auto-merge then uses
`https://github.example.com/api/graphql`. The startup token check (`/user`) does not apply to
an App; the mint is the check.

For git, give the Pipeline's `spec.git.secretRef` Secret the same three keys instead of
`token`: git-clone and git-push then use an installation token as the HTTPS password
(`x-access-token`), minted at the controller's `--scm-api-url` and shared by every Pipeline
that names the same App.

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
| `api` | Full API access — required for MR creation, comments, label updates, and deleting the source branch of an MR kardinal closed, or of a step that ended before it opened an MR |

A **project access token** with `api` scope is recommended over a personal access token
for production deployments.

Git pushes use the token in the Pipeline's `spec.git.secretRef` Secret, not this one. That
token's user (or the project access token's role) needs the **Maintainer** role on the project.
Environments without `pr-review` push straight to `spec.git.branch`, and GitLab lets only
Maintainers push to a protected default branch. With Developer, those environments fail at
git-push with "pre-receive hook declined". Developer is enough only if the branch protection lets
Developers push. The controller token needs at least Developer, to open and close merge requests
and delete their branches.

### Webhook configuration

1. In your GitLab project, go to **Settings → Webhooks**.
2. Set **URL** to `http://<controller-host>:8083/webhook/scm`.
3. Set **Secret token** to the same value as `--webhook-secret`.
4. Enable **Merge request events**.
5. Click **Add webhook**.

> GitLab validates webhooks by comparing the `X-Gitlab-Token` header against the
> configured secret (plaintext comparison, not HMAC).

GitLab refuses webhooks to private and local addresses by default. When the controller's
webhook URL is one (an in-cluster Service, a private load balancer), a GitLab administrator
must allow it: **Admin → Settings → Network → Outbound requests → Allow requests to the local
network from webhooks and integrations** (the application setting
`allow_local_requests_from_web_hooks_and_services`). Without it GitLab blocks the webhook,
and merges are seen only by polling.

### Repository URLs

Use project URLs that end in `.git`, in the Pipeline's `spec.git.url` and in the GitOps
tool's source (for example the Argo CD Application `repoURL`). Without `.git`, GitLab
answers git requests with a `301` redirect, which Argo CD does not follow. kardinal drops
the `.git` when it names the project for the API, so subgroup paths such as
`https://gitlab.example.com/group/sub/app.git` work.

### "PR" means merge request

kardinal uses "PR" for GitLab merge requests too: in the comments it posts (for example
"kardinal closed this PR: ..."), in step messages, in the `prNumber` and `prURL` step
outputs, in the PRStatus resource and in the `bundle.pr` gate attributes.

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
| `write:repository` | Create and close pull requests, add labels, delete the head branch of a PR kardinal closed, or of a step that ended before it opened a PR |

Create an API token in your Forgejo/Gitea instance under **Settings → Applications → Access Tokens**.
The startup token check cannot see these scopes; see [Token check at startup](#token-check-at-startup).

Deleting a branch on Forgejo and Gitea also closes every open pull request from it, from a queue,
shortly after the delete call returns. kardinal deletes the head branch of a PR it closed. It keeps
the branch of a PromotionStep deleted on its own while its Bundle is `Promoting` or `Failed`, its
Graph is still there, and the new step pushes at once. kro creates that step again, and the new
step opens its PR from the same branch name about a second later, so deleting the branch would
close that PR too. When the new step would wait (a gate is not ready, the Pipeline is paused, or an
upstream step is not `Verified`) or would not come, kardinal deletes the branch.

### Webhook configuration

1. In your Forgejo/Gitea repository, go to **Settings → Webhooks → Add Webhook → Gitea**.
2. Set **Target URL** to `http://<controller-host>:8083/webhook/scm`.
3. Set **Secret** to the same value as `--webhook-secret`.
4. Select **Pull Request** events.
5. Click **Add Webhook**.

> Forgejo/Gitea signs webhooks with HMAC-SHA256 (same algorithm as GitHub). Forgejo
> sends the signature in `X-Forgejo-Signature` and `X-Gitea-Signature`; Gitea sends
> `X-Gitea-Signature`. The controller accepts any of them.

A merge event carries the merge commit in `pull_request.merge_commit_sha`, and the controller
records it with the merge, so the `argocd` and `flux` health checks need no further API call for it.

Forgejo and Gitea deliver webhooks only to hosts their `ALLOWED_HOST_LIST` setting allows,
and its default, `external`, excludes private and loopback addresses. When the controller's
webhook URL is one (an in-cluster Service, a private load balancer), the server blocks the
delivery: the controller never receives the event, logs no `webhook received`, and merges are
seen only by polling. An administrator must allow the controller's host (a name, IP or CIDR;
`private` allows every private address) in `app.ini` or the container's environment. Forgejo
reads the list from `[webhook]` (`FORGEJO__webhook__ALLOWED_HOST_LIST`), where `*` allows every
host. Gitea 28 reads it from `[security]` (`GITEA__security__ALLOWED_HOST_LIST`); it rejects
`*`, and the `[webhook]` key only logs a deprecation error, so list the hosts, for example
`*.svc.cluster.local`. The live suites set both this way (`hack/e2e/components/giteafamily.sh`).

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
and repository write access (repository write deletes the head branch of a PR
kardinal closed, or of a step that ended before it opened a PR). The controller sends it as a Bearer token, so app passwords do not work.
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

## SSH git authentication

git-clone and git-push can use ssh instead of HTTPS with every provider: set the Pipeline's
`spec.git.url` to an ssh URL (`ssh://git@host:port/owner/repo.git` or `git@host:owner/repo.git`)
and put an ssh key and the server's host keys in its `spec.git.secretRef` Secret:

```bash
ssh-keygen -t ed25519 -N '' -f kardinal-deploy-key      # add kardinal-deploy-key.pub as a deploy key with write access
ssh-keyscan -p 22 github.com > known_hosts               # check the keys against the provider's published fingerprints
kubectl create secret generic git-ssh -n <pipeline-namespace> \
  --from-file=sshPrivateKey=kardinal-deploy-key \
  --from-file=knownHosts=known_hosts
```

| Key | Purpose |
|---|---|
| `sshPrivateKey` | The private key, PEM or OpenSSH format, without a passphrase. The ssh user is the URL's (`git@`), or `git`. |
| `knownHosts` | `known_hosts` lines for the server. **Required**: kardinal never accepts an unknown host key, and only offers the host key algorithms recorded for the host (`[host]:port` for a port other than 22). |

Connecting and the ssh handshake are bounded (30s), and a push waits at most a minute for the
server's post-receive hooks before it closes the connection. A step that is cancelled or times
out closes its ssh connection at once. A wrong or missing host key fails the step with a `knownhosts:` error, and a Secret with an ssh
URL but no `sshPrivateKey` or `knownHosts` fails it with a message naming the missing key. The
config source of a config Bundle on the same ssh host uses the same key. With the chart's
`networkPolicy.enabled`, allow the ssh port (22, or the server's) in `networkPolicy.extraEgress`:
the default egress rules allow 443 and 6443 only.

Every git connection, ssh and HTTPS alike, fails after 5 minutes without a byte sent or
received, so a server that stalls in the middle of a clone or push fails the step (with an
`i/o timeout` error, retried like other git errors) instead of holding a controller worker.
A transfer that keeps moving data is not cut, however long it takes.

PRs, labels, merge detection and the other SCM API calls still use the controller's token or
GitHub App (`--scm-provider`); ssh only replaces git's transport. The repository is read from
the ssh URL the same way (`owner/repo`, the GitLab project path, or `v3/org/project/repo` on
Azure DevOps).

---

## Token check at startup

When a token is set, the controller checks it once at startup, in the background, and
logs what it finds. This also runs in Helm installs, where the token comes from the
watched Secret. The check never stops the controller from starting.

| Provider | Call | Logged as a warning |
|---|---|---|
| GitHub / GitHub Enterprise | `GET /user` | token rejected (401); a classic PAT without `repo` or `public_repo`; a fine-grained PAT or GitHub App token, whose permissions the call cannot show |
| GitLab | `GET /api/v4/personal_access_tokens/self` | token rejected; no `api` scope |
| Forgejo / Gitea | `GET /api/v1/user` | token rejected (401). The API does not show a token's scopes, so a missing scope is not reported. `/user` needs `read:user`, which the documented scopes leave out, so it usually returns 403; an info line then says "SCM token scopes not checked" with the provider name |
| Bitbucket Cloud, Azure DevOps | none | not checked; an info line says so, and token problems show on the first promotion step |

Find the warnings with:

```bash
kubectl logs -n kardinal-system -l app.kubernetes.io/name=kardinal-promoter \
  | grep "SCM TOKEN SCOPE WARNING"
```

A network or HTTP error from the check is logged at debug level only (a Forgejo or Gitea
403 is the info line above). The token itself is never logged. A token loaded later by the
Secret watcher (after a rotation) is not checked.

---

## Credential rotation (zero-downtime)

kardinal-promoter supports rotating SCM credentials at runtime without restarting the controller or
causing a gap in active promotions.

### How it works

When the controller is configured to read the token from a Kubernetes Secret
(via `--scm-token-secret-name` or the Helm `github.secretRef.name` value), a background
`SecretWatcher` polls that Secret every **30 seconds**. When the token value (or, for a
[GitHub App](#github-app), any of its three keys) changes, the
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
the `helm upgrade` in [Upgrade](installation.md#upgrade) with `--set github.token=<NEW_TOKEN>` added.

### Rotating a PAT (zero-downtime procedure)

1. Generate the new token in your SCM provider and copy it.
2. Update the controller's token Secret: the Secret `github.secretRef.name` names, or
   `<release>-github-token` when you set `github.token` (then use the `helm upgrade` above
   instead):
   ```bash
   kubectl create secret generic <token Secret> \
     --namespace kardinal-system \
     --from-literal=token=<NEW_TOKEN> \
     --dry-run=client -o yaml | kubectl apply -f -
   ```
3. Update every Secret that a Pipeline's `spec.git.secretRef` names and that holds the
   old token. Git clone and push do not use the controller's Secret: each step reads the
   `token` key of the Pipeline's Secret, in the Pipeline's namespace. If that Secret still
   holds the revoked token, git-clone fails with "authentication required".
   ```bash
   kubectl create secret generic <pipeline-git-secret> \
     --namespace <pipeline-namespace> \
     --from-literal=token=<NEW_TOKEN> \
     --dry-run=client -o yaml | kubectl apply -f -
   ```
4. Within 30 seconds the controller picks up the change. No controller restart is needed.
   Promotions in flight are not interrupted — the atomic swap completes before the next
   reconcile iteration reads the token. The next git step reads the Pipeline Secret again.
5. Verify the rotation took effect by checking the controller log:
   ```bash
   kubectl logs -n kardinal-system -l app.kubernetes.io/name=kardinal-promoter --tail=20 \
     | grep "SCM credentials rotated"
   ```
   The watcher logs this line only when it sees the token change after startup, so a line
   with a `time` later than your Secret update means the controller now uses the new token.
   When the controller starts, it logs "SCM credentials loaded" for its first read of the
   Secret instead. That line confirms the new token only if the Pod started after you
   updated the Secret. Revoke the old token only after one of these checks passes.

### Static mode (development / CI)

If you run the controller with `--github-token` (or only the `GITHUB_TOKEN` env var) and no
`--scm-token-secret-name`, no Secret watching is configured. To rotate the token you must
restart the controller:

```bash
kubectl rollout restart deployment/kardinal-promoter -n kardinal-system
```

Update the Pipeline git Secrets too, as in step 3 above.

---

## Adding a new SCM provider

Implement the `SCMProvider` interface in `pkg/scm/` and register it in
`pkg/scm/factory.go`:

```go
func NewProvider(providerType, token, apiURL, webhookSecret string) (SCMProvider, error) {
    token = strings.TrimSpace(token)
    switch providerType {
    case "github", "":
        return NewGitHubProvider(token, apiURL, webhookSecret), nil
    // gitlab, forgejo/gitea, bitbucket, azuredevops ...
    case "myscm": // your provider
        return NewMySCMProvider(token, apiURL, webhookSecret), nil
    default:
        return nil, fmt.Errorf("unknown SCM provider type %q: ...", providerType)
    }
}
```

Implement `scm.BranchDeleter` and `scm.MergeCommitGetter` too. `DynamicProvider` forwards them
to your provider when it implements them. Without them, kardinal leaves the head branch of a PR it
closed, and cannot look up a merge commit the webhook did not carry. Add the provider's
signature header to `webhookSignatureHeaders` and its event header, if it sends one, to
`webhookEventHeaders` in `pkg/scm/repo_url.go`.
