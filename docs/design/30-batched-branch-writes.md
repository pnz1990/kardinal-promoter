# Design: one write for the environments that are ready together on one branch (#2366)

> Status: **Proposed (2026-10-21)**, for review. Nothing here is built. v0.14 work, behind the
> `v0.14-hold` label.
> Created: 2026-10-21
> Depends on: [10-graph-first-architecture.md](10-graph-first-architecture.md),
> [16-graph-capability-ledger.md](16-graph-capability-ledger.md) (G8, G11, G14) and
> [21-receipts.md](21-receipts.md).
> Verified against: this repository at `main` (`0a19a24d1`). Source references are `path:line`
> in that tree. The measurements are #2366's run of `TestCompact_Shape300` (e2e-live run
> 39528603407, Kubernetes 1.37, #2389's counters).

---

## 1. Problem, as measured

`TestCompact_Shape300` promotes one Bundle through 300 `approval: auto` environments: 30 waves
of 10, every environment an overlay of one repository on one branch. The run took 1,085.7 s.

- **No contention.** 0 pushes were refused, 0 rebases were made and 0 fresh clones were
  restarted. 300 clones took 334.3 s (1.11 s each) and 300 pushes took 198.1 s (0.66 s each),
  and every push landed first time.
- **The writers take turns.** The controller's branch queue (`branchQueue`,
  `pkg/reconciler/promotionstep/branch_queue.go:20-48`, #1578) gives the auto promotions writing
  one branch the turn one at a time. A step takes the turn before `git-clone` and gives it back
  once `git-push` lands (`reconciler.go:1157-1181`). So a wave of 10 is 10 turns in a row.
- **A turn costs about 3.5 s.** The 300 turns ran in about 1,060 s. Of a turn, the clone is 31%
  (1.11 s), the push 19% (0.66 s), and the rest (about 1.8 s) is the other serialized steps
  (`kustomize-set-image`, `git-commit`), the status writes, and handing the turn on: the wake,
  the requeue and the cache check.
- **Waves grow from 24.4 s (waves 1-5) to 37.6 s (waves 26-30).** The wave's longest clone grows
  with it, from about 1.1-1.6 s to 1.7-2.3 s, as the repository gains ten commits a wave (§9).

So the cost is **one full turn per environment**, not rebases. A writer queue already exists
(the issue's option 2). This note designs the issue's option 1: **the environments of one
Bundle that are ready together on one branch are written in one turn: one clone, one commit per
environment, one push.** The cheaper clone (option 3) is a separate item in §9.

**Goal (#2366's "Done when"):** a wave of ten costs well under its current 24-38 s, and the
per-wave time stays flat as the repository grows, measured by `TestCompact_Shape300`'s wave log
on the same runner class. Every environment keeps its own evidence.

## 2. What must not change

| Per environment, today | Where |
|---|---|
| One PromotionStep, its state machine and `status` (`outputs.commitSHA`, `branch`, steps, health) | `reconciler.go:1049-1324`, `recordPushedCommit` `:1556-1596` |
| One commit per environment, message `[kardinal] Promote <bundle> to <env>` (+ Bundle, Pipeline, Namespace lines), which a retried push recognises (`ErrAlreadyCommitted`) | `pkg/steps/steps/git_commit.go:43-53, 80-94`, `pkg/scm/git_client.go:325-332` |
| One PromotionReceipt: `change.commit` is the environment's `outputs.commitSHA` | `pkg/reconciler/promotionstep/receipt.go:376-378`, `pkg/receipt/statement.go:284-291` |
| One AuditEvent per transition | `transition.go:200-250` |
| Health checked per environment, ancestry `descends(commitSHA)` on a branch many environments push to | `reconciler.go:1898-2192`, ledger G8 (`16-…:434`) |
| A failure (health, retries) fails that environment only; its siblings go on; later waves wait | `bundle/reconciler.go:1746-1760`, `compact.go:339, 425-427` |
| A gate or hold keeps an environment from starting; nothing gated is written | `compact.go:339-346`, `reconciler.go:956-989` |

pr-review environments get no PR body from this design. They push their own branch
`kardinal/<ns hash>/<bundle>/<env>` and take no turn (`branch_queue.go:180-206`). They are
**out of scope**, and so are `layout: branch` environments (their RenderRun Job pushes) and the
`argocd` strategy (no git write).

## 3. Decision: one commit per environment in one push, not one commit

The issue proposed one commit for the whole wave. **I propose one commit per environment,
stacked, and one push.** The saving is the same: one clone, one push and one turn handoff for
the wave. A local commit costs milliseconds, and a push of ten small commits costs about what a
push of one does. But every row of §2 stays as it is:

- each environment's `outputs.commitSHA` is its own commit;
- each receipt's `change.commit` names a commit that changes only that environment's files;
- each commit message is today's, so retries recognise their own commit;
- a revert or an audit of one environment touches one commit.

A single shared commit would make ten receipts name one SHA, so each would also need the list of
files that are its environment's. It would make `git revert` all-or-nothing. And it would need a
new way for a retried write to recognise its commit. None of that buys time.

## 4. Mechanism

The design follows the RenderRun pattern the compact shape already uses for
`layout: branch` (`pkg/graph/compact_render.go:13-36`):

1. the step asks for the work in its own status;
2. the Graph creates (or updates) the CR that does it;
3. that CR's reconciler does the git work in one reconcile and writes only its own status;
4. the Graph mirrors the result onto the step's `spec.live`, which the step reconciler reads
   (`promotionstep_types.go:95-104`: "The reconciler reads only this copy, never the source
   objects").

### 4.1 The CRD that owns the batch: BranchWrite

A new namespaced CRD, **BranchWrite**: the writes of one Bundle's environments to one
repository branch. One object per Bundle and `normalizeRepoURL(url)#branch`, created by the
Graph, named `<bundle>-<hash(url#branch)>`.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: BranchWrite
spec:
  bundleRef: {name: podinfo-abc12}          # literal, from the Graph build
  git: {url: …, branch: main, secretRef: …} # the Pipeline's
  environments:                             # literal: every auto environment on this branch
    - name: w01-r0
      step: podinfo-podinfo-w01-r0-…        # the PromotionStep name (promotionStepK8sName)
      update: {strategy: kustomize, path: environments/w01-r0}   # what its update steps did
    - …
  requested:                                # rendered by the Graph from StepsObserved (§4.2)
    - {step: podinfo-podinfo-w01-r0-…, uid: …, at: "2026-10-21T02:00:01Z"}
status:
  round: 3                                  # the last round started
  inFlight: {round: 3, members: [w01-r0, …], pushIntent: "…"}   # frozen before the clone
  environments:                             # one entry per environment written, keyed by step UID
    w01-r0: {uid: …, requestedAt: …, round: 3, commitSHA: …, noChanges: false, error: ""}
```

- `spec.environments` is fixed at the Graph build, from the same inputs `renderRunSpec` uses
  today. It covers 300 entries of name, step and path at about 150 bytes each (45 KB): one copy
  in the Graph's data def, one in the object. The 300-environment Graph is 514 KB now; the guard
  is 1.2 MB (G10).
- `spec.requested` is the only part that changes.
- The reconciler writes only `status`.

### 4.2 How a step asks to be written

For an environment whose Pipeline opts in (§7), the sequence
`git-clone, <update>, git-commit, git-push` is replaced by one step, **`await-write`**:
- `await-write, health-check`;
- `config-merge` Bundles are excluded at first (§8).

- **First run.** `await-write` outputs `writeRequested=true`. The reconciler sets
  `status.writeRequestedAt` in the same patch, as `renderRequestedAt` is set today
  (`reconciler.go:1196-1202`, `pkg/steps/defaults.go:20-23`). The step returns `StepPending`.
- **The Graph lists the request.** The BranchWrite's template renders `spec.requested` from
  `StepsObserved`: the steps that have `status.writeRequestedAt`, are on this branch, and are
  not yet in the BranchWrite's `status.environments` with a matching `requestedAt`. `StepsObserved`
  is the selector ref the compact shape already re-walks on every step write
  (`compact.go:396-414`).

A step never takes the branch turn itself any more: `basePushBranch` (`branch_queue.go:180-206`)
finds no `git-push` in its sequence.

### 4.3 How the batch is written: one round

The BranchWrite reconciler runs a **round** when `spec.requested` holds a step that is not
written yet:

1. **Take the branch turn.** It takes the same `branchQueue` key (`normalizeRepoURL(url)#branch`),
   so it is serialized with every other writer of the branch in this controller: other
   Bundles' BranchWrites, and unbatched steps of other Pipelines. The turn is the existing G8
   exception, extended to BranchWrite (§6).
2. **Freeze the members.** It writes `status.inFlight = {round: n+1, members}` before any git
   work. `members` is `spec.requested` at that moment, at most `maxRoundMembers` (50, a bound on
   one reconcile's work). A request that arrives later goes into the next round.
3. **Check the Bundle.** It runs the push guard once, since one BranchWrite is one Bundle. A
   Superseded or Rejected Bundle ends the round with every member marked `cancelled`, which is
   what each step's guard does today (`push_guard.go`).
4. **Clone once, then commit per member.** It makes one clone (depth 1, as `git-clone`). For
   each member in `spec.environments` order, it runs the member's update (the same
   `kustomize-set-image` / `helm-set-image` / `yaml-update` code, on the member's path) and
   commits it with that environment's `PromoteMessage`, plus a trailer
   `Kardinal-Write: <branchwrite>/<round>`.
   - A member whose update fails gets `error` and no commit; the others go on.
   - A member with nothing to change gets `noChanges: true` and the clone's head, as
     `recordPushedCommit` does today (#1669).
5. **Push once.** It records `pushIntent`, then pushes the branch without force.
   - **On a non-fast-forward** (another writer: a person, CI, another shard), it fetches the new
     head and re-runs the members' updates on it. These updates are deterministic (set this
     image on this path), so redoing them is the rebase. It re-commits and pushes, up to
     `maxRebaseAttempts` (6), and then retries the round with backoff, as `ErrContended` does now
     (`reconciler.go:1475-1494`).
   - **When the other writer changed a member's file**, that member is redone on their version,
     which is today's fresh-clone restart, without the clone.
6. **Record.** For each member it writes `status.environments[<env>] = {uid, requestedAt,
   round, commitSHA | noChanges | error}`, and clears `inFlight`.

**After a crash.** A crash after the push but before step 6 leaves `inFlight` with
`pushIntent`. The next reconcile fetches the branch's recent history (the 200 commits a rebase
already fetches, `git_rebase.go:64-68`). If it finds commits with the round's trailer, it
records them; otherwise it runs the round again. The commit message plus its trailer is the
idempotency key, as the message is for a retried push today.

**What it cannot do:** the clone is the reconcile's only working tree. Nothing of it crosses a
reconcile, which is G8's rule for git work (`16-…:425`). Everything a later reconcile needs is
in `status`.

### 4.4 How a step learns its write landed

The PromotionSteps template renders **`spec.live.write`** from the BranchWrite, read back
through a `ref`. This is how `spec.live.renders` is rendered from `refRenderRuns` today
(`compact_render.go:146-…`). It is filtered to the step's environment and keeps the entry only
when its `uid` is the step's and its `requestedAt` is the step's `writeRequestedAt`.

`await-write` reads only `spec.live.write`:

| `spec.live.write` | `await-write` returns | Then |
|---|---|---|
| none yet | `StepPending` | waits (requeue, and re-walk on the mirror) |
| `commitSHA` | `StepSuccess`, outputs `commitSHA`, `branch`, `writtenBy: <branchwrite>/<round>` | `recordPushedCommit` keeps it, then HealthChecking as today |
| `noChanges` | `StepSuccess`, `noChanges=true` | as today's `noChanges` (#1669) |
| `error` | `StepFailed`, retryable | the step's retry budget. A retry sets a new `writeRequestedAt`, which is a new request, so a new round |
| `cancelled` | the step's halt path | Superseded or Rejected, as today |

So a step decides only from its own spec and status. The BranchWrite decides only from its own
spec. The data moves through CRD fields and the Graph.

### 4.5 The Graph nodes (compact shape)

| Node | Kind | Content |
|---|---|---|
| `BranchWriteData` | `def` | per branch: name, git, `environments` (literal) |
| `BranchWrites` | template per branch (a handful per Pipeline), or a `forEach` over `BranchWriteData` | the BranchWrite: literal spec plus `requested` from `StepsObserved` |
| `refBranchWrites` | selector `ref` | the BranchWrites kro applied for this Bundle, read back for `spec.live.write` |

A BranchWrite is not admitted per wave. It exists from the Graph's start and is idle until a
step asks. It holds no wave index: whoever asks between two rounds is written together. That
covers fleets too, whose pacing decides which steps exist and so which ask.

## 5. Failure, gates and holds

- **A gate not ready.** The environment gets no step (`compact.go:339-346`), so nothing asks to
  be written. An environment whose gate turns ready later asks later and is written in a later
  round. **The worst case is today's cost**: one round per environment, when gates open one at a
  time.
- **A hold (`spec.holds`, pause, freeze, slot, hooks).** These keep an existing step waiting
  before `await-write` runs (`lifecycle.go:36-135`), so it does not ask.
  - A hold that arrives after the step asked but before its round froze: the step withdraws its
    request by clearing `writeRequestedAt` when the hold is applied, and the Graph drops it from
    `spec.requested`.
  - A hold that arrives after the freeze does not stop that round. That is today's semantics too:
    a step re-checks holds only at the start of a reconcile, and its write finishes within one
    (`reconciler.go:1063-1071`).
- **One member's update fails** (a path that is gone, a bad image reference): that member gets
  `error`, the others commit and push. The failed one retries on its own budget, then fails as
  today.
- **The push fails for every member** (credentials, a server error): every member gets `error`
  and retries on its own budget. Credentials keep today's `gitCredentialRetries` rule. The
  BranchWrite has no retry budget of its own: it acts only when asked.
- **Health fails after the write.** Each member's HealthChecking is its own
  (`reconciler.go:1898-2192`): one environment goes `Failed`, `AbortedByAlarm` or `RollingBack`.
  The others go on, the Bundle turns Failed, and the next wave does not start. All of this is
  exactly today's behaviour.
  - An `onHealthFailure: rollback` makes a new rollback Bundle (`createAutoRollback`). Its steps
    ask its own BranchWrite, and the rollback commit changes only that environment's files, as
    now.
- **Supersession.** A newer Bundle has its own BranchWrite. The two are serialized by the branch
  turn, and the older one's next round is cancelled by the push guard (§4.3 step 3).

## 6. Graph-first check

| Rule (`10-graph-first-architecture.md:61-127, 189-200`) | This design |
|---|---|
| A Watch node, or an Owned node whose reconciler writes its status | BranchWrite is an owned node; its reconciler writes only `BranchWrite.status` |
| No cross-CRD status mutation | The step writes its `status.writeRequestedAt`; the BranchWrite writes its status; the Graph mirrors one to the other's spec (like `spec.live.gates`, `mirror.go:25-71`) |
| No decision on fields outside its own CRD | `await-write` reads `spec.live.write`; the BranchWrite reads its `spec.requested` |
| No in-memory state between reconciles | The working tree lives in one reconcile. `inFlight` and the trailer cover a crash. The branch turn is the existing G8 exception (`16-…:431`), which "times requeues only"; a BranchWrite taking it changes that row's holder list and needs the same owner approval |
| `time.Now()` only in a status write | `requestedAt` and `pushIntent` are written to status |
| No subprocess, no external HTTP in a hot path | go-git, as `git-clone` and `git-push` today |

Constraints of kro, and how the design meets each:

- **G11 (collections are all-or-nothing).** A `forEach` of BranchWrites with one bad item would
  block every branch. A plain template per branch avoids that: one or two per Pipeline.
- **G14 (one pending field makes a node Unresolved).** `requested` reads only `StepsObserved`,
  which resolves from the start (an empty list), so the BranchWrite resolves before any step
  exists.

## 7. API and rollout

- **Opt-in:** `Pipeline.spec.git.writes: perEnvironment | batched`, default `perEnvironment`.
  `batched` applies only to environments it can apply to (§2's scope). Others keep their
  sequence, and the Pipeline's status says which are batched.
- **Phase 1:** the compact shape (more than 100 environments, or fleets), where the cost
  matters.
- **Phase 2:** the node-per-environment shape, with one BranchWrite node per branch and the same
  mirror through each step node's `spec.live`.
- **Receipts:** unchanged in schema: `change.commit` is the environment's own commit. A
  `change.write: "<branchwrite>/<round>"` field (optional) would show the environment was
  written with others. I'd leave it out until someone asks.
- **Metrics:** `kardinal_git_operations_total` and `kardinal_git_operation_duration_seconds`
  (#2389) count the BranchWrite's clones, rebases and pushes as they count the steps' today. A
  new `kardinal_branch_write_round_members` histogram shows how much batching each round gets.

## 8. Not covered, and what would break it

- **Scope.**
  - `config-merge` (config and mixed Bundles) reads a second clone at the config source commit
    (`git_clone.go:92-119`). It is excluded from phase 1: those environments keep their own
    sequence.
  - StepTemplate custom steps in the sequence are excluded too.
- **One more Graph walk per round.** A round's result reaches the steps through kro (BranchWrite
  status, then the re-walk, then the mirror patch, then the step's reconcile). That is a few
  seconds per round, but once per round instead of once per environment.
- **Members that share a file** (two environments with one path, a misconfiguration) cannot
  both be committed in one round. The second is put in the next round, which makes the overlap
  visible in its status.
- **Two controller shards writing one branch** get no turn between them, as today. Their pushes
  race, and the round's rebase handles it.
- **Expected saving:** a wave of ten becomes one turn of about one clone (1.1 s), ten updates
  and commits (tens of milliseconds), one push (0.7 s) and one handoff (about 1.8 s): about 4 s
  instead of 24-38 s. To be measured by `TestCompact_Shape300`, as #2366's "Done when" says.
  The one handoff still includes the Graph walk above.

## 9. Separate item: a cheaper clone

This is independent of §3-8. It helps every writer: unbatched steps today, and the BranchWrite's
one clone per round.

- **Measured.** The clone is 31% of a turn (1.11 s mean), and the wave's longest clone grows from
  about 1.1-1.6 s to 1.7-2.3 s over 30 waves. `GoGitClient.Clone` is already `Depth: 1,
  SingleBranch: true` (`pkg/scm/git_client.go:226-261`), so it transfers one tree whatever the
  history.
  - **The growth is therefore unexplained.** It might be the git server generating a shallow pack
    over a longer history or more packfiles.
  - **First step: measure where it grows.** Time a depth-1 clone of the test repository against
    its commit count, on Forgejo and from a local `file://` copy, to tell server work from
    transfer.
- **Option A: a per-repository cache** in the controller's cache directory (an `emptyDir`): a
  bare repository per `normalizeRepoURL(url)`.
  - Under the branch turn, `git-clone` first fetches the branch into the cache. That is
    incremental: only the commits since the last turn.
  - It then makes the working tree from the cache locally, with no network. It checks that the
    result's head is the remote head it just fetched; otherwise it falls back to today's clone.
  - Correctness never depends on the cache: a missing, corrupt or stale cache falls back to a
    fresh clone.
  - Bounds: an LRU of repositories with a disk cap. A restart starts empty.
  - It is process state across reconciles, like `remoteCache` (`remote_cache.go`), with no
    promotion state in it. It needs a G8 row and the owner's approval, as the branch turn did.
- **Option B: keep the last turn's working tree** for the branch and fetch, reset and clean it
  for the next turn. It is faster than A (no checkout), but a dirty or half-written tree is
  harder to rule out. I'd prefer A.
- **Expected:** most of the 1.1 s and of the slope. That is about 30% of a turn unbatched; batched,
  a share of the single per-round clone.

## Appendix A: what was checked in the source

- Branch turn: `branch_queue.go:20-48, 180-236`, and `reconciler.go:1157-1181`.
- Compact admission (gates, holds, upstreams): `compact.go:339-346, 425-427`.
- The RenderRun request and mirror this follows: `compact_render.go:13-145`, `reconciler.go:1196-1202`.
- `spec.live` and its rule: `promotionstep_types.go:95-104`.
- The GateMirror patch: `mirror.go:25-71`.
- commitSHA recording: `reconciler.go:1556-1596`.
- Receipt change: `receipt.go:376-378`.
- Health failure: `reconciler.go:2264-2347`.
- Failed Bundle and later waves: `bundle/reconciler.go:1746-1760`.
- Clone depth: `git_client.go:226-261`.
- G8 rows: `16-…:425, 431, 434`.
