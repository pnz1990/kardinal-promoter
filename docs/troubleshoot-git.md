# Git and SCM problems

Clone, push, pull request and SCM API errors. Start with
[`kardinal doctor` and `kardinal explain`](troubleshooting.md#start-here-kardinal-doctor).

## Git errors

### Symptom: "waiting for its turn to push to main: other promotions of this controller are writing it"

Not an error. Automatic promotions of one controller that write the same
branch of the same repository take turns: one clones, commits and pushes at a
time, and the others wait without cloning. A wide wave of environments
on one branch makes about one push per environment instead of racing for the
branch. A waiting step uses no retry and gives its worker back, at low priority,
so steps of other Pipelines and namespaces are not held up behind the wave. Turns go oldest first: when one ends, the oldest waiting step is woken at once
and the others keep their place (each asks again after about its place in line
times the length of a turn, at most 15 seconds). The turn is per repository
branch, whatever the namespace: two teams whose Pipelines write one branch take
turns, because their pushes would collide. PR-review promotions push to their
own `kardinal/` branches and do not wait.

### Symptom: "base branch ... moved while promoting"

In an `approval: auto` environment, something else (another Pipeline, Renovate, Dependabot, a CI job) pushed to the base branch while the step was promoting. `git-push` first replays the promotion's files onto the new head and pushes again, up to 6 times. When the other writer changed the same files, the branch keeps moving, or the shallow clone lacks the commit the promotion was made on (`the clone lacks the commit to rebase from`), the step starts again from a fresh clone, up to 3 times in one reconcile. After that the message reads `(gave up after 3 restarts in this reconcile)` and the step is retried with a jittered backoff (at most 2 minutes), so writers that collided do not collide again: `retrying in <delay> (3, no limit while other writers keep moving the branch)`. These retries are counted in `status.contendedRetries`, not `status.retryCount`: contention alone never fails the step, it only slows it down. A missing base commit is the exception. `git-push` first fetches the branch's last 200 commits to find it, and counts each fresh clone it still needs in `status.outputs.baseMissingRestarts`. After 5 of them, the step fails with `fresh clones lacked the commit to rebase from`. That message usually means the branch was force-pushed or rewritten during the promotion. `pr-review` environments push to their own `kardinal/<namespace hash>/<bundle>/<env>` branch and do not hit this.

### Symptom: "push ... was refused as non-fast-forward, but the remote branch did not move"

The git server refused the push, yet the base branch is still at the commit the promotion was built on, so no other writer is involved. Common causes are a stale lock file in the server's repository, a read-only repository or protected branch rule that the server reports as non-fast-forward, or a full disk on the git server. This is not contention: the step is retried with the normal limit (`status.retryCount`, 5 retries) and then fails. Fix the repository on the git server, then retry the promotion.

### Symptom: "base branch main moved from ... to ...; rebuilt the PR branch"

Not an error. While a `pr-review` PR waits for its merge, the controller compares the base branch head with the commit the promotion was built on (`status.outputs.baseSHA`) every 30 seconds. When the new commits on the base changed a file under the environment's path (or a Helm `valuesFile` outside it), or the base was force-pushed, it runs the promotion's steps again from a fresh clone of the new head and force-pushes the PR branch. The PR keeps its number; `status.outputs.prBranchRebuilds` counts the rebuilds and the step emits a `PRBranchRebuilt` Event. A base that moved at other paths only updates `baseSHA`. So does a base that already has the promotion's change, typically the PR's own merge, seen before the PR's status reports it. That is not counted as a rebuild. A host that dismisses approvals on a new push (GitHub's "Dismiss stale pull request approvals") asks for the review again after a rebuild. When a rebuild fails, the message reads `rebuilding its branch on main at <sha> failed, retrying` and the PR stays as it was.

### Symptom: "its branch has commits kardinal did not push ... so it is not rebuilt"

Someone pushed to the PR branch (`kardinal/<namespace hash>/<bundle>/<env>`) after kardinal did, so its head is no longer `status.outputs.pushedSHA`. kardinal never overwrites such a commit: the PR is not rebuilt on the moved base. Merge it as it is, or resolve it by hand.

### Symptom: "authentication required" or "authorization failed" on git clone or push

See [SCM provider failures](#scm-provider-failures): the token is missing, expired or has no write
access.

## SCM provider failures

### Symptom: `git push origin <branch>: authorization failed`

The remote refused the push (HTTP 403): the token has no write access to the repository or lacks the required scope. An expired or revoked token gives `authentication required` instead (HTTP 401).

git uses the Secret named by the Pipeline's `spec.git.secretRef` (key `token`), in the Pipeline's namespace. PR and API calls use the controller's token, set with the chart's `github.secretRef` in the controller's namespace. The controller reloads it when the Secret changes.

```bash
# Which Secret does git use?
kubectl get pipeline my-app -o jsonpath='{.spec.git.secretRef.name}'

# Verify token scope — must have 'repo' scope (or 'contents:write' for fine-grained tokens)
# Test the token directly:
TOKEN=$(kubectl get secret <secret> -o jsonpath='{.data.token}' | base64 -d)
curl -s -H "Authorization: token $TOKEN" https://api.github.com/user | jq .login
```

To rotate the token, update the Secret in place (here `github-token` in the Pipeline's namespace):
```bash
kubectl create secret generic github-token \
  --from-literal=token=<new-token> \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl label secret github-token kardinal.io/referenceable=true --overwrite
```

A step that returns an error is retried with backoff (10s, 20s, 40s, 80s, then 2m) up to 5 times; `status.message` shows `retrying in <d> (<n>/5)` and `status.nextRetryAt` when the retry runs. The step waits for it even when something else reconciles it first, such as a gate re-evaluation or a PR status change. Rotate the token within that window and the step continues. After the last retry the PromotionStep is Failed; create a new Bundle to promote again.

### Symptom: "authentication required" with "git Secret ... not found" or "spec.git.secretRef is not set"

git has no token for the Pipeline's HTTPS remote: `spec.git.secretRef` names a Secret that does not exist (or has no `token` key), or is not set. The git error is followed by what is missing:

```
retrying in 20s (2, no limit while git has no credentials) after error: step git-push: git push origin main: authentication required: Unauthorized (git Secret team-a/github-token not found)
```

The PromotionStep's `GitCredentialMissing` condition is `True` with the same message, and one `Warning` Event with reason `GitCredentialMissing` is emitted. Such a step is not failed after 5 retries: it retries every 2 minutes until git has a token. These retries are counted in `status.gitCredentialRetries`, not in `status.retryCount`, so another error, before or after the Secret exists, still gets its 5 retries. Create the Secret in the Pipeline's namespace (and set `spec.git.secretRef` if it is not set), and the step continues at its next retry:

```bash
kubectl create secret generic github-token -n team-a --from-literal=token=<token>
kubectl label secret github-token -n team-a kardinal.io/referenceable=true
```

### Symptom: "authentication required" with "git Secret ... could not be read"

The controller could not read the Secret that `spec.git.secretRef` names, for example because its
ServiceAccount may not get Secrets in the Pipeline's namespace (`forbidden`) or the API server
timed out. The message names the Secret and the read error:

```
retrying in 20s (2/5) after error: step git-push: git push origin main: authentication required: Unauthorized (git Secret team-a/github-token could not be read: secrets "github-token" is forbidden: ...)
```

The `GitCredentialMissing` condition is `True` with reason `SecretUnreadable`, with one `Warning`
Event. Creating the Secret does not fix this, so the step keeps the limit of 5 retries and then
fails. Fix the read error (for `forbidden`, the controller's RBAC: the chart grants `get` on
Secrets in every namespace the controller reconciles), then create a new Bundle to promote again.

### Symptom: "get PR status failed: HTTP 401", "HTTP 403" or "HTTP 404" on a PromotionStep

`wait-for-merge` fails the step at once, instead of polling, when the SCM API answers:

- **401**: the SCM token was rejected (expired or revoked). Rotate the token as shown above.
- **403** (not a rate limit): the token has no access to the repository or its pull requests.
  Give it read access to pull requests.
- **404 or 410**: the repository or the PR does not exist, or the token cannot see it. Check
  `spec.git.url` on the Pipeline and that the PR was not deleted.

Polling again cannot fix these, so the step is marked as a permanent failure. A 403 rate limit,
429, 5xx or network error keeps the step waiting and it is retried every 30 seconds.

A step already in `WaitingForMerge` learns about the merge from its PRStatus. When the PRStatus
poll gets one of these errors, it writes it to `status.pollError` and the step fails with
`PR #<n> cannot be polled: <error>`:

```bash
kubectl get prstatus -o custom-columns=NAME:.metadata.name,PR:.spec.prNumber,ERROR:.status.pollError
```

The PRStatus keeps polling every 5 minutes and clears `pollError` once a poll succeeds, but the
failed step does not resume. Fix the token or the repository, then create a new Bundle.

### Symptom: "403 rate limit exceeded" or "429 Too Many Requests" in controller logs

GitHub's API rate limit (5000 req/hr for authenticated requests) or GitLab's rate limit has been hit. The **SCM circuit breaker** handles this automatically.

**How the circuit breaker works:**
1. Each repository owner (GitHub user or org, GitLab top-level group, Bitbucket workspace, Azure DevOps organization) has its own circuit. After 5 consecutive failures (5xx or a network error) for that owner's repositories, its circuit opens and its SCM calls are blocked for a cooldown period. Calls for other owners go on
2. An exhausted rate limit (`X-RateLimit-Remaining: 0` or `RateLimit-Remaining: 0`, a 429, or a 403 with `Retry-After`) belongs to the token, so it opens one shared circuit at once and every owner waits for the reset
3. The first cooldown is 5 seconds. Each failed probe doubles it (5, 10, 20, 40, 80 s), up to 2 minutes. Requests that were in flight when the circuit opened, and fail or succeed afterwards, are not counted. The cooldown respects `X-RateLimit-Reset`, `RateLimit-Reset` and `Retry-After` response headers when present
4. After the cooldown, one probe request is allowed (half-open state). Other callers are told to look again in 2 seconds. A probe that gets no answer frees the slot after 45 seconds (the providers' 30-second HTTP timeout and a margin). **Recovery time:** the circuit closes on the first probe after the SCM is back, so promotions resume within the cooldown in force when the outage ended, plus up to a fifth more for the steps' jitter. For a 60-second outage that cooldown is 40 seconds, so steps resume within about 50 seconds of the recovery. However long the outage, they resume within 2.5 minutes
5. On probe success, the circuit closes and normal operation resumes
6. A token rotation (a new value in the token Secret) keeps the circuits: the new token is first used by the probe
7. A step that meets an open circuit waits for it and runs again. The wait does not count against the step's 5 retries: its message says "waiting ... for the SCM (not counted as a retry ...)", the condition `SCMUnavailable` is True, `status.scmWaitSince` says since when, and one `SCMUnavailable` Warning Event is written. The wait is bounded: the step fails ("the SCM was unavailable for ...") once it has waited the environment's `stepTimeoutSeconds`, or else the controller's `--scm-wait-timeout` (default 30 minutes). A superseded step closing its PR waits the same way, so its PR and `kardinal/` branch are not left behind, and after the bound it spends its close retries. PRStatus polls wait for the circuit too
8. Once the bound fails a step, `status.scmWaitSince` is cleared and `SCMUnavailable` is False with reason `TimedOut`
9. Metrics: `kardinal_scm_circuit_state{provider,owner}` is `0` closed, `1` half-open, `2` open, with `owner="_quota"` for the token's rate-limit circuit, and `kardinal_scm_requests_total{result="circuit_open"}` counts the calls an open circuit refused ([SCM API and git metrics](reference/metrics.md#scm-api-and-git-metrics))

**Checking circuit state in logs:**

```bash
# Look for SCM calls the open circuit blocked
kubectl logs -n kardinal-system deploy/kardinal-promoter | grep "SCM circuit open"

# The error the blocked call returns, also shown in PromotionStep messages:
# github scm: SCM circuit open until 2026-04-17T05:30:00Z
```

**Manual recovery if circuit stays open too long:**

```bash
# Restart the controller to reset in-memory circuit state
kubectl rollout restart deployment/kardinal-promoter -n kardinal-system
```

**Check current GitHub rate limit:**

```bash
# The controller's token: the Secret named by the chart's github.secretRef.name
TOKEN=$(kubectl get secret github-token -n kardinal-system -o jsonpath='{.data.token}' | base64 -d)
curl -s -H "Authorization: token $TOKEN" https://api.github.com/rate_limit | jq .rate
```

**Long-term fix:** Use a GitHub App token (higher rate limits than PAT).

### Symptom: Push succeeds but PR is not opened

Check the controller logs for the PR creation call:
```bash
kubectl logs -n kardinal-system deploy/kardinal-promoter | grep "open-pr\|pull_request" | tail -20
```

Common causes:
- The base branch (`spec.git.branch`, default `main`) does not exist in the GitOps repo
- The environment already runs this version. `git-commit` finds nothing to change, so no PR is opened, and the PromotionStep has `status.outputs.noChanges: "true"`. Its `status.outputs.commitSHA` is the branch head it cloned, and the health check waits until the GitOps tool has applied that commit (`waiting for <sha>`): a GitOps source that is suspended or pinned to an older revision keeps the step in `HealthChecking` until `health.timeout`
- The GitOps repo is private and the token lacks `repo` scope
