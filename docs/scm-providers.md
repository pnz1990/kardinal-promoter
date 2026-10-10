# SCM Providers

kardinal-promoter supports multiple Source Control Management (SCM) providers for
pull request and merge request lifecycle operations. The provider is configured on the
controller at startup.

With the Helm chart, set `scm.provider` and `scm.apiURL`. Put the token in a Secret named by
`github.secretRef.name`, and the webhook secret in one named by `webhook.secretRef.name` (key
`secret`). The chart passes these to the controller as the flags and environment variables
below.

## Supported Providers

| Provider | `--scm-provider` value | PR type | Webhook header checked | PR labels | Approvals read | [`kardinal/gates` status](pr-evidence.md#gate-status-check-kardinalgates) |
|---|---|---|---|---|---|---|
| GitHub / GitHub Enterprise | `github` (default) | Pull Requests | `X-Hub-Signature-256` (HMAC-SHA256) | Yes | Yes | Commit status |
| GitLab (incl. subgroups) | `gitlab` | Merge Requests | `X-Gitlab-Token` (shared token) | Yes | Yes | Commit status (name `kardinal/gates`) |
| Forgejo / Codeberg | `forgejo` | Pull Requests | `X-Forgejo-Signature` or `X-Gitea-Signature` (HMAC-SHA256) | Yes | Yes | Commit status |
| Gitea | `gitea` | Pull Requests | `X-Gitea-Signature` or `X-Hub-Signature-256` (HMAC-SHA256) | Yes | Yes | Commit status |
| Bitbucket Cloud | `bitbucket` | Pull Requests | `X-Hub-Signature` (HMAC-SHA256) | No (Bitbucket has no PR labels) | Yes | Build status (key `kardinal/gates`) |
| Azure DevOps | `azuredevops` | Pull Requests | `X-AzureDevOps-Token` (custom header you add to the service hook) | Yes (PR tags) | Yes | PR iteration status (genre `kardinal`, name `gates`) |
| Bitbucket Data Center / Server | `bitbucket-datacenter` | Pull Requests | `X-Hub-Signature` (HMAC-SHA256) | No (no PR labels) | Yes | Not posted yet |

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

Bitbucket Cloud, Azure DevOps and Bitbucket Data Center are newer and less tested than GitHub and GitLab: the e2e suites cannot host them, so they are checked against their documented REST APIs only.
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
| `repo` | Create/close pull requests, post comments, read PR status, set the `kardinal/gates` commit status, delete the head branch of a PR kardinal closed, or of a step that ended before it opened a PR |

These are classic token scopes. With a fine-grained personal access token or a GitHub App
token, deleting the head branch is a git refs call and needs the **Contents: read and write**
repository permission, besides the pull request permissions kardinal already needs; without it
every PR kardinal closes ends with its step asking you to delete the branch by hand. The
`kardinal/gates` status needs **Commit statuses: read and write**.

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
| `write:repository` | Create and close pull requests, add labels, set the `kardinal/gates` commit status, delete the head branch of a PR kardinal closed, or of a step that ended before it opened a PR |

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

Use a PAT with **Code (Read & write)** (with **Code (Status)** if your organization splits it, for the `kardinal/gates` pull request status). The repository is `org/project/repo`, taken
from `spec.git.url` (`https://dev.azure.com/org/project/_git/repo`).

Webhook: create a **Web Hooks** service hook for **Pull request updated**, set the URL
to `http://<controller-host>:8083/webhook/scm`, and add the HTTP header
`X-AzureDevOps-Token: <value of --webhook-secret>`. A PR counts as merged only when its
status is `completed`.

---

## Bitbucket Data Center

```bash
kardinal-controller \
  --scm-provider bitbucket-datacenter \
  --scm-api-url https://bitbucket.example.com \
  --github-token $BITBUCKET_HTTP_ACCESS_TOKEN \
  --webhook-secret $KARDINAL_WEBHOOK_SECRET
```

`--scm-api-url` is required: the server's base URL, with its context path if it has one
(`https://example.com/bitbucket`). The controller uses REST API 1.0 (Bitbucket Server and Data
Center 7.x and later).

Use an **HTTP access token** (personal, project or repository) with **Repository write**
(project admin for a project token is not needed). The controller sends it as a Bearer token.
For git over HTTPS, put the same token in the Pipeline's git Secret; a personal token needs your
username in the URL (`https://alice@bitbucket.example.com/scm/PLAT/web-app.git`). Or use
[ssh](#ssh-git-authentication) (`ssh://git@bitbucket.example.com:7999/plat/web-app.git`).

The repository is the project key and slug from `spec.git.url`: an HTTP clone URL
(`/scm/PLAT/web-app.git`), a browse URL (`/projects/PLAT/repos/web-app/...`) or an ssh URL all
work, and personal repositories (`/scm/~alice/web-app.git`, `/users/alice/repos/web-app`) too.
Keys and slugs are matched without case, so the ssh URL's lower-case key matches webhooks.
In `scm.allowedRepositories`, name a repository as `host/KEY/slug` on the host of `scm.apiURL`
(`bitbucket.example.com/PLAT/*`, `bitbucket.example.com/~alice/*` for personal repositories):
every URL form of the repository matches it ([the shared SCM token](guides/security.md#the-shared-scm-token-and-scmallowedrepositories)).
`kardinal validate --allowed-repositories <list> --scm-provider bitbucket-datacenter` checks a
file the same way.
A ScmProvider or ClusterScmProvider of `type: bitbucket-datacenter` (with `apiURL`, the
server's base URL) works the same way. Its `allowedRepositories` are `KEY/slug` globs, and they
match every URL form of the repository.

- **PRs**: opened from `kardinal/<bundle>/<env>`; a PR that is open between the same branches is
  reused. Closing declines it (with the PR's current version) and deletes the branch with the
  branch-utils API. There are no PR labels, so find rollback PRs by their `[kardinal] Rollback`
  title.
- **Approvals**: reviewers with status `APPROVED` count; one with `NEEDS_WORK` blocks
  `bundle.pr.<env>.isApproved`.
- **Merge commit**: read from the PR's `properties.mergeCommit`, which Bitbucket Data Center
  returns for a merged PR though its OpenAPI description leaves it out (the webhook carries it
  too). Without it the `argocd` and `flux` health checks fall back as described in
  [When the merge commit is not known yet](health-adapters.md#when-the-merge-commit-is-not-known-yet).

Webhook: **Repository settings → Webhooks → Create webhook**, URL
`http://<controller-host>:8083/webhook/scm`, **Secret** the value of `--webhook-secret`, events
**Pull request: Merged** (and optionally Declined). Bitbucket signs it in `X-Hub-Signature`.

---

## Pipeline CRD configuration

A Pipeline without `spec.git.providerRef` uses the controller's SCM provider, chosen by
`--scm-provider` (or `KARDINAL_SCM_PROVIDER`). To use another SCM, or another token, set
`spec.git.providerRef` to a [ScmProvider or ClusterScmProvider](#several-scm-providers-scmprovider-and-clusterscmprovider).
`spec.git.provider` is **deprecated and ignored**: it does not select a provider, and the
CRD accepts only `github` or `gitlab` there. Leave it unset. The repository comes from
`spec.git.url`.

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
config source of a config Bundle on the same ssh host uses the same key, and so do the
controller's reads of the branch while a PR waits: its heads (`ls-remote`) and recent history,
which it uses to follow or rebuild the PR branch and to check a later synced commit in health.
With the chart's
`networkPolicy.enabled`, allow the ssh port (22, or the server's) in `networkPolicy.extraEgress`:
the default egress rules allow 443 and 6443 only.

`layout: branch` works over ssh too: the render Job gets the Secret's `sshPrivateKey` and
`knownHosts` keys (not `token`) and dials only the ssh host and port of `spec.git.url`. With
`render.networkPolicy.enabled`, add that port to `render.networkPolicy.gitEgress`
([Rendered manifests](rendered-manifests.md)).

Every git connection, ssh and HTTPS alike, fails after 5 minutes without a byte sent or
received, so a server that stalls in the middle of a clone or push fails the step (with an
`i/o timeout` error, retried like other git errors) instead of holding a controller worker.
A transfer that keeps moving data is not cut, however long it takes.

PRs, labels, merge detection and the other SCM API calls still use the controller's token or
GitHub App (`--scm-provider`); ssh only replaces git's transport. The repository is read from
the ssh URL the same way (`owner/repo`, the GitLab project path, or `v3/org/project/repo` on
Azure DevOps).

## PR controls

An environment's `pr` field ([Customising the PR](pr-evidence.md#customising-the-pr)) sets the
PR's title, body, labels, reviewers and assignees, and can enable auto-merge. What each
provider applies:

| Control | GitHub | GitLab | Forgejo / Gitea | Bitbucket Cloud | Azure DevOps | Bitbucket Data Center |
|---|---|---|---|---|---|---|
| `titleTemplate`, `bodyTemplate` | Yes | Yes | Yes | Yes | Yes | Yes |
| `labels` | Yes | Yes | Yes | No (no PR labels) | Yes (PR tags) | No (no PR labels) |
| `reviewers` | Usernames | Usernames | Usernames | Account IDs or `{UUID}`s | Identity IDs | Usernames |
| `teamReviewers` | Team slugs (organisation repos) | No | Team names (organisation repos) | No | Group identity IDs | No |
| `assignees` | Usernames | Usernames | Usernames | No (no PR assignees) | No (no PR assignees) | No (no PR assignees) |
| `merge.auto` | Auto-merge (GraphQL `enablePullRequestAutoMerge` / `disablePullRequestAutoMerge`) | Auto-merge (`auto_merge`, or `merge_when_pipeline_succeeds` before GitLab 17.11; cancelled with `cancel_merge_when_pipeline_succeeds`) | Scheduled merge (`merge_when_checks_succeed`; cancelled with `DELETE .../merge`) | No (no auto-merge API) | Auto-complete (cleared to turn it off) | Auto-merge (`POST .../merge` with `autoMerge: true`, 8.15 and later, enabled in the repository's auto-merge settings; `DELETE .../auto-merge` cancels it) |
| `merge.method` | `merge`, `squash`, `rebase` | `merge`, `squash` (a rebase merge is the project's merge method setting) | `merge`, `squash`, `rebase` | — | `merge` (no fast-forward), `squash`, `rebase` | `merge` (`no-ff`), `squash`, `rebase` (`rebase-no-ff`); the strategy must be enabled on the repository |
| `merge.commitMessageTemplate` | Yes | Yes (merge and squash commits) | Yes | — | Yes | Yes |

A control the provider does not apply fails the step before the PR is opened, with a message
such as `environment prod: pr.teamReviewers is not supported by the gitlab SCM provider`.

What auto-merge needs, and what "nothing pending" means, on each provider. With nothing
pending the SCM would merge at once, so kardinal leaves the PR for a merge by hand
(`prAutoMerge: failed`) unless `pr.merge.allowImmediate` is set:

- **GitHub**: the repository must allow auto-merge (**Settings → General → Allow auto-merge**).
  Nothing pending: GitHub refuses auto-merge on a PR in "clean status" (no required check or
  review pending); with `allowImmediate` kardinal merges it with the REST merge endpoint, which
  applies branch protection too. The token needs **Contents: Read and write** and **Pull
  requests: Read and write**.
- **GitLab**: nothing pending: the MR's `detailed_merge_status` is `mergeable`. While GitLab is
  still checking a new MR (`checking`, `unchecked`), kardinal tries again later. A project whose
  merge method is merge commit makes a merge commit over the squash commit; both get the message.
- **Forgejo / Gitea**: nothing pending: no commit status that is still pending or failing, and
  no base branch protection that requires approvals or status checks. Forgejo and Gitea run a
  scheduled merge when the checks succeed or an approval arrives.
- **Bitbucket Data Center**: nothing pending: the mergeability check reports no merge check
  vetoing the merge. Otherwise kardinal requests auto-merge (`autoMerge: true` on the merge
  call), and Bitbucket merges the PR once its checks pass. Auto-merge needs Bitbucket Data
  Center 8.15 or later with auto-merge turned on in the repository's (or project's) settings;
  without it the call is refused (403, "auto-merge is disabled for this repository") and the PR
  waits for a merge by hand (`prAutoMerge: failed`).
- **Azure DevOps**: nothing pending: no branch policy evaluation queued, running or rejected.
  Auto-complete is set by the token's identity, which opened the PR, and completes the PR once
  its branch policies pass.

kardinal keeps the PR's head branch when the SCM merges it, as it does for a merge by hand.

---

## Several SCM providers: ScmProvider and ClusterScmProvider

One controller can open PRs on several SCMs, or on one SCM with several tokens. Each
Pipeline that should not use the controller's `--scm-provider` names a provider in
`spec.git.providerRef`:

- A **ScmProvider** is namespaced. Only Pipelines in its namespace can use it, and its
  Secrets must be in that namespace too.
- A **ClusterScmProvider** is cluster-scoped. Pipelines in the namespaces its
  `spec.allowedNamespaces` label selector matches can use it. If the selector is not set,
  no namespace can use it, so a cluster-wide token is shared only on purpose. It names
  the namespace of its Secrets, which can be any namespace: only cluster administrators
  can create ClusterScmProviders (see [Install modes and RBAC](#install-modes-and-rbac)).

Every Secret a provider names, `secretRef` and `webhookSecretRef` alike, must be labeled
`kardinal.io/referenceable: "true"`. A provider whose Secret does not have the label is
not used, and its `Ready` condition says why. With the label, the Secret's owner decides
that custom resources may use the Secret. Without it, anyone who can create a
ScmProvider could send any Secret of the namespace to an `apiURL` they choose.

```bash
kubectl -n team-a create secret generic team-gitlab-token --from-literal=token=glpat-...
kubectl -n team-a label secret team-gitlab-token kardinal.io/referenceable=true
```

```yaml
apiVersion: kardinal.io/v1alpha1
kind: ScmProvider
metadata:
  name: team-gitlab
  namespace: team-a
spec:
  type: gitlab                       # github, gitlab, forgejo, gitea, bitbucket or azuredevops
  apiURL: https://gitlab.example.com # https:// only (see below); empty uses the public API
  secretRef:
    name: team-gitlab-token          # key "token" unless secretRef.key is set
  webhookSecretRef:                  # optional; key "secret" unless set
    name: team-gitlab-webhook
  allowedRepositories:               # optional; globs over the SCM repository path
    - platform/*
    - apps/**
  # instanceSigners: [Forgejo]       # Forgejo/Gitea only: the instance's SIGNING_NAME / SIGNING_EMAIL
---
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
  namespace: team-a
spec:
  git:
    url: https://gitlab.example.com/platform/my-app-deploy
    secretRef: { name: team-gitlab-git }   # git clone and push, as before
    providerRef:
      name: team-gitlab                    # kind defaults to ScmProvider
  environments:
    - name: prod
      approval: pr-review
```

```yaml
apiVersion: kardinal.io/v1alpha1
kind: ClusterScmProvider
metadata:
  name: github-enterprise
spec:
  type: github
  apiURL: https://ghe.example.com/api/v3
  secretRef: { name: ghe-token, namespace: kardinal-system }
  allowedNamespaces:
    matchLabels:
      scm.example.com/ghe: "true"
# In a Pipeline: providerRef: { kind: ClusterScmProvider, name: github-enterprise }
```

How it works:

- **The provider is set when the Graph is built.** When the controller translates a
  Bundle, it resolves `providerRef` and writes the provider's kind, name and UID into
  each PromotionStep's `spec.scmProvider`. The PromotionStep copies it into the PRStatus
  of its PR. Opening the PR, labels, polling, comments and branch cleanup all use that
  provider's client. If the provider is missing, does not allow the namespace or the
  repository, or uses an `http://` API without the opt-in, the Bundle stays `Available`
  with a `TranslationError` that gives the reason. It is retried every 15 seconds and goes
  ahead once the provider is created or fixed.
- **A step keeps the provider it started with.** If the provider is deleted, or deleted
  and created again under the same name (so it has another UID), the step fails with the
  reason. It does not fall back to the controller's provider, because that SCM does not
  know the PR. The same happens when `allowedRepositories` stops allowing the repository,
  or when a ClusterScmProvider stops selecting the namespace.
- **Every use is checked again.** Each SCM call re-checks the provider's UID, its
  `allowedRepositories` and, for a ClusterScmProvider, the namespace's labels against
  `allowedNamespaces`. The controller caches a namespace's labels for 30 seconds, so a
  namespace whose label is removed loses the provider within 30 seconds, including for
  steps and PRs already in flight. A provider's Secrets are also cached for 30 seconds:
  a rotated token is used within 30 seconds, and a change to a provider's `spec` is used
  at the next call. **When you rotate a provider's token, keep the old token valid for at
  least 30 seconds after you update the Secret**, so calls made from the cached Secret do
  not fail. A deleted provider's client and its cached Secrets are dropped from memory, and
  a read of its Secret still in flight then is not cached. A slow read of an older Secret or
  Namespace never replaces a newer one in the cache (the higher `resourceVersion` wins, and
  without one the read that started later), so a rotation or a removed label is not undone by
  it.
- **`allowedRepositories`** lists globs over the repository path the SCM API uses, such
  as `owner/repo` or `group/subgroup/repo`, on the provider's host. Matching ignores
  case. `*` matches one path segment, and a trailing `/**` matches any depth below. A
  segment with `%`, `\`, `..` or whitespace never matches. The globs are matched like
  `scm.allowedRepositories` (see [Security](guides/security.md)). The check runs when the
  Graph is built, and again on every SCM call the provider's token makes. If the field
  is empty, every repository is allowed. The controller-wide `scm.allowedRepositories`
  applies to the controller's own token only. A Pipeline with `providerRef` and its own
  `git.secretRef` never uses that token.
- **`apiURL` must be `https://`.** An `http://` URL would send the token in clear text,
  so it is refused unless the cluster administrator sets
  `scm.providersAllowInsecureHTTP: true` (`--scm-providers-allow-http`) for an
  in-cluster SCM without TLS. Every provider API request goes through the controller's
  egress guard, the one NotificationHooks and MetricChecks use: no loopback, link-local
  or cloud metadata addresses, even after a redirect or a DNS change.
- **Signed commits are checked with it too.** A Pipeline with
  `imageVerification.commits.requireSigned` asks the provider, with its token and the same
  checks, whether the config commit is signed, and the repository must be on the provider's
  host. For Forgejo and Gitea, `instanceSigners` lists the names or emails the instance signs
  commits with, as `--scm-instance-signers` does for the controller's provider; the
  controller's list is not used for a provider. See
  [Signed commits](image-verification.md#signed-commits).
- **Git credentials do not change.** `git-clone` and `git-push` still use
  `spec.git.secretRef` (or an ssh remote). The provider's token is used only for the SCM
  API.
- **Status.** The controller sets the `Ready` condition to `True` when the provider's
  `apiURL`, `allowedRepositories` and `allowedNamespaces` are valid and its Secrets exist,
  are labeled `kardinal.io/referenceable`, and have their keys. Otherwise it is `False`
  with the reason. Secrets are not watched, so the controller checks them again every 5
  minutes. It makes no SCM call before a Pipeline needs one.
- **Only token authentication.** A provider uses an API token. The [GitHub App](#github-app)
  mode is for the controller's own GitHub credential (`github.secretRef`), not for an
  ScmProvider or ClusterScmProvider.

### Webhooks per provider

Each provider has its own webhook endpoint. Deliveries are checked with the provider's
`webhookSecretRef` and mark only the PRStatuses of PRs opened on that provider:

| Provider | Webhook URL |
|---|---|
| ScmProvider `<name>` in namespace `<ns>` | `POST http://<controller-host>:8083/webhook/scm/namespaces/<ns>/<name>` |
| ClusterScmProvider `<name>` | `POST http://<controller-host>:8083/webhook/scm/cluster/<name>` |
| The controller's `--scm-provider` | `POST http://<controller-host>:8083/webhook/scm` (PRs of Pipelines without `providerRef` only) |

Configure the webhook on the SCM as described in that provider's section above, with
this URL and the provider's webhook secret. If a provider has no `webhookSecretRef`, its
endpoint answers 401 to every delivery, just like an endpoint for a provider that does
not exist. Its merges are still seen by polling.

Before the signature is checked, the endpoint reads only the provider and its webhook
Secret. The Secret read is cached for 30 seconds, whether the Secret is found or not.
The token and the SCM are used only to confirm a signed merge event. Each endpoint of a
provider that exists takes 10 deliveries a second, with bursts of 20. Past that it answers
429. GitHub does not redeliver a delivery that got 429, and other SCMs may not either; the
merge is not lost, because the PRStatus poll sees it at its next interval. A delivery for a
provider that does not exist gets 401 without using a rate-limit bucket, so deliveries to
made-up names do not push out the buckets of real endpoints.

### Install modes and RBAC

The chart lets the controller read ScmProviders and ClusterScmProviders and write their
status. The controller never creates or changes their spec. Secrets are read with `get`
by name, like every Secret the controller reads. In namespace mode
(`controller.watchNamespace`), the controller can read Secrets only in the watched
namespace. A ClusterScmProvider used there must keep its Secrets in that namespace.

Who may create a ScmProvider in a namespace decides which token that namespace's
Pipelines can use. Grant `create` on `scmproviders` like `create` on Secrets. Grant
`clusterscmproviders` to cluster administrators only.

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
   # A Secret a Pipeline names stays labelled referenceable (docs/guides/security.md).
   kubectl label secret <pipeline-git-secret> --namespace <pipeline-namespace> \
     kardinal.io/referenceable=true --overwrite
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
