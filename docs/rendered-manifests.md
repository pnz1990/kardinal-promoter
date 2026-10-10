# Rendered Manifests

With `layout: branch`, kardinal renders an environment at promotion time and commits the plain
YAML to that environment's own branch. The DRY source (a Kustomize overlay or a Helm chart) stays
on `spec.git.branch`. Argo CD or Flux syncs the rendered branch, which holds only plain manifests.

Teams use this pattern when PR reviewers need to see the exact YAML that will be applied.

## Why render at promotion time

- **Visible PR diffs.** A promotion PR shows the rendered change (`image: ghcr.io/org/app:1.29.0`,
  a new environment variable, a changed resource limit), not a one-line `values.yaml` edit.
- **GitOps agent performance.** Argo CD and Flux apply plain YAML; they do not run `kustomize
  build` or `helm template` on every reconcile.
- **CODEOWNERS on rendered output.** Ownership rules can name individual rendered files.
- **Auditability.** The history of `env/prod` is the sequence of manifests that ran in production,
  and every commit says which DRY commit it was rendered from.

## Repository structure

```
main (DRY source, spec.git.branch):
  base/
    kustomization.yaml
    deployment.yaml
    service.yaml
  environments/
    test/kustomization.yaml     # overlay: namespace, images, patches
    prod/kustomization.yaml

env/test (rendered, written by kardinal):
  .kardinal/rendered.yaml       # render marker (see Drift)
  my-app-test_deployment-my-app.yaml
  my-app-test_service-my-app.yaml

env/prod (rendered):
  .kardinal/rendered.yaml
  my-app-prod_deployment-my-app.yaml
  my-app-prod_service-my-app.yaml
  CODEOWNERS                    # yours: kardinal keeps files it did not write
```

Each rendered object is one file at the root of the branch: `<kind>-<name>.yaml` for a
cluster-scoped object, `<namespace>_<kind>-<name>.yaml` for a namespaced one.

## Pipeline configuration

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
spec:
  git:
    url: https://github.com/myorg/gitops-repo
    branch: main                   # the DRY source
    secretRef: {name: github-token}
  environments:
    - name: test
      path: environments/test      # rendered with kustomize build
      layout: branch               # rendered into env/test
      health:
        type: argocd
    - name: prod
      path: environments/prod
      approval: pr-review          # the PR targets env/prod; its diff is rendered YAML
      layout: branch
      render:
        branch: env/prod           # default env/<name>
        onDrift: fail              # default; or overwrite
      health:
        type: argocd
```

`layout: branch` on `spec.git` applies to every environment. Each environment needs its own
rendered branch, different from `spec.git.branch`; otherwise the Pipeline is `Ready=False`
(reason `ValidationFailed`) and its Bundles fail. `update.strategy: argocd` cannot be combined with
`layout: branch`.

### Helm charts

An environment path holding a `Chart.yaml` is rendered with `helm template`:

```yaml
    - name: prod
      path: charts/my-app
      layout: branch
      update:
        strategy: helm             # helm-set-image edits the DRY values before the render
        helm:
          valuesFile: values-prod.yaml
          imagePathTemplate: .image.tag
      render:
        helm:
          releaseName: my-app      # .Release.Name, default the Pipeline name
          namespace: my-app-prod   # .Release.Namespace, default the environment name
          valuesFiles: [values-prod.yaml]   # over values.yaml; default update.helm.valuesFile
```

The render is offline: subcharts must be vendored in `charts/`, `lookup` finds nothing,
`getHostByName` resolves nothing, and the default capabilities are used. Hooks and `NOTES.txt`
are not rendered. A template that calls a function whose result changes from one render to the
next (`randAlphaNum` and the other `rand*`, `uuidv4`, `now`, `ago`, `genCA`, `genPrivateKey` and
the other `gen*`, `encryptAES`, `bcrypt`, `htpasswd`, `shuffle`) fails the render: the same DRY
commit would render different manifests, every promotion would commit a change, and a rollback
would not restore what ran. Set `render.allowNondeterministic: true` to allow them. `env` and
`expandenv` are not available, as in Helm.

A `chart` Bundle (a chart version from a [Helm Subscription](subscription.md#promoting-a-chart-version))
cannot promote a `layout: branch` environment yet: the Bundle fails at build. Pipelines that
render always use the node Graph shape: the compact shape (`kardinal.io/graph-shape: compact`,
or more environments than `--graph-compact-above`) refuses `layout: branch`.

## What a promotion does

Rendering never runs in the controller. The controller's step waits for the environment's
**RenderRun**, a Kubernetes Job in the Pipeline's namespace that does the clone, the render, the
commit and the push:

| Where | Step | What it does |
|---|---|---|
| controller | `render` | Asks for the render (`status.renderRequestedAt`); the Bundle's Graph then creates the RenderRun, and the step waits for it (`kubectl get renderruns`) |
| render Job | `git-clone` | Clones `env/<name>` (the first time it creates the branch, with only the render marker) and checks out the DRY source next to it |
| render Job | `kustomize-set-image` | Edits the image in the DRY overlay (`helm-set-image` for a chart). The DRY source is never committed to |
| render Job | `render-manifests` | Renders the environment path, checks `env/<name>` for drift, writes one file per object and the render marker |
| render Job | `git-commit` | Commits to `env/<name>`, with the DRY commit in the message trailers |
| render Job | `git-push` | Pushes `env/<name>`, or `kardinal/<bundle>/<env>` for a PR |
| controller | `open-pr`, `wait-for-merge` | For `approval: pr-review`, a PR into `env/<name>` whose diff is the rendered YAML |
| controller | `health-check` | The Argo CD Application or Flux Kustomization that syncs `env/<name>`; for `approval: auto` it waits for the commit the Job pushed |

The RenderRun's `status.result` (and the step's `status.outputs`) record the commit pushed, the
DRY commit rendered, the renderer, the object count and the marker digest. A failed render fails
the step and the Bundle with the RenderRun's message; promote a new Bundle to render again.

### The render Job

The RenderRun reconciler creates the Job, owned by the RenderRun, from the `kardinal-render` image
(Helm `render.image`, controller flag `--render-image`):

- it reads the DRY source and pushes the render with the Pipeline's own git Secret
  (`spec.git.secretRef`), never the controller's credentials. Only the keys its `spec.git.url` uses
  are mounted, read-only: `token` for an `https://` URL, `sshPrivateKey` and `knownHosts` for an ssh
  one (`ssh://` or `git@host:path`, see [SSH git authentication](scm-providers.md#ssh-git-authentication)). A key the Secret lacks
  keeps the Pod from starting, and the RenderRun message says so;
- it runs as the ServiceAccount `kardinal-render` (`render.serviceAccountName`), which the
  controller creates in the namespace without a token and binds to no role, with
  `automountServiceAccountToken: false`: the Pod has no Kubernetes credentials at all;
- non-root (uid 65532), read-only root filesystem, no privilege escalation, every capability
  dropped, the `RuntimeDefault` seccomp profile, no service links, and an `emptyDir` for its work;
- no network but git to the Pipeline's repository: inside the render process every request through
  Go's default HTTP transport is refused (so a generator or transformer that fetches a URL reaches
  nothing), and git dials only the host and port of `spec.git.url` (port 22 for an ssh URL without
  one), through the egress guard (no loopback, link-local or cloud metadata addresses), never
  through a proxy. Over ssh the host key must be in `knownHosts`;
- CPU and memory limits (`render.resources.limits`, default 1 CPU and 512Mi) and an
  `activeDeadlineSeconds` (`render.timeout`, default 5m). A render that runs out of memory fails
  with `the render ran out of memory (512Mi) and was stopped`; one that runs out of time is ended by
  Kubernetes, and the reconciler gives up on it 2 minutes later;
- a Pod that fails for a reason other than the render (evicted or preempted: the
  `DisruptionTarget` condition) does not count, other failures are retried up to 3 times, and a
  render that failed for good (a template error, drift, a refused input; exit code 2) fails at once;
- a Pod that cannot start says why in the RenderRun message (`ImagePullBackOff`,
  `Unschedulable`, ...). The chart's `imagePullSecrets` are added to the render Pods
  (`--render-image-pull-secrets`); they must exist in the Pipeline namespace;
- `render.networkPolicy.enabled` adds, in each namespace of `render.networkPolicy.namespaces`, a
  NetworkPolicy that lets the render Pods reach DNS and the git hosts of
  `render.networkPolicy.gitEgress` only (needs a CNI that enforces NetworkPolicy; open the ssh
  port of the git host there for an ssh `spec.git.url`). It is off by
  default, and **recommended**: it is the boundary outside the render process.

The result comes back through the Pod's termination message, which the RenderRun reconciler
copies to `status.result` only from a Pod the Job owns, and only when it can be true: a full
commit id on the rendered branch (or the promotion branch of a `pr-review` step), rendered from a
full DRY commit. The step then reads the branch head on the remote (`git ls-remote`) and fails if
it is not the reported commit. A result that pushed nothing (the branch already held the render)
names the rendered branch's head too, which is checked the same way, and its marker digest must
be one of the environment's recorded renders (`status.knownMarkerDigests`); that head is the
commit the health check waits for, so the environment is Verified only once the GitOps tool has
applied it. A finished RenderRun never runs again; a RenderRun is never run in
the controller's own namespace.

A render whose Job pushed but whose result was lost (its Pod gone, or a result that could not be
read) fails, and the next render of the environment knows that Bundle as unconfirmed
(`status.unconfirmedBundles`, only the newest such render): if the rendered branch's marker names
it and its files are unchanged, the marker is accepted as kardinal's instead of failing as drift,
and a rollback to that Bundle trusts its render. A Job Pod that pushed and was retried before it
wrote its result (its node drained, say) finds its own marker in the same way: the marker of the
same Bundle and DRY commit, with its files unchanged, is accepted, and the retry reports the commit
the first attempt pushed.

The rendered commit's message ends with:

```
Kardinal-Dry-Commit: 4f2c...
Kardinal-Dry-Path: environments/prod
Kardinal-Bundle: my-app-v1-29-0
```

### Which DRY commit is rendered

- A Bundle with `configRef.commitSHA` (a config or mixed Bundle, or an image Bundle that pins its
  DRY commit) renders that commit. `configRef.gitRepo` must be empty or the Pipeline's repository.
- A rollback Bundle renders the DRY commit its target was rendered from in this environment. The
  render Job looks through the last 500 commits of the rendered branch for a render kardinal made
  of the target Bundle: a commit whose `Kardinal-Bundle` trailer names it and whose
  `Kardinal-Dry-Commit` is a full 40-character commit id, and whose tree holds a render marker for
  this Pipeline, environment, Bundle and DRY commit with every file it lists unchanged, and, when
  the environment has a record of its renders, a marker digest in it. Other
  commits that name the Bundle are skipped. The DRY commit must also be on `spec.git.branch`: one
  that is not (a commit on another branch, pushed by someone who can write to the rendered branch)
  is refused. If no such render is there, the step fails (pin the commit with
  `configRef.commitSHA`). `kardinal rollback` therefore restores the old manifests, not the old
  image over today's DRY source.
- Otherwise the head of `spec.git.branch` is rendered. The step records the commit in
  `status.outputs.dryCommit`.

### Determinism and limits

kustomize (`sigs.k8s.io/kustomize/api`) and Helm (`helm.sh/helm/v4`) run inside the render Job, at
the versions in `go.mod`; the image ships no `kustomize`, `helm` or `git` binary, and a render
never runs a command. Before anything is rendered:

- the whole DRY source is read into memory: a symbolic link anywhere in it is refused, and it may
  hold at most 64 MiB and 20,000 files. The Helm chart is loaded from that copy (its
  `.helmignore` applies), never from disk;
- every value of every kustomization is checked, whatever the field: a remote reference (any
  `://`, `git@...`, `?ref=`, `github.com/...`) is refused in `resources`, `components`, `bases`,
  generator `files` and `envs`, patch paths, `openapi`, `crds`, `replacements` and every other field
  kustomize loads from, and in every generator, transformer and validator configuration a
  kustomization names (a file of the DRY source, or inline). Only data fields may hold a URL:
  generator `literals`, `commonAnnotations`, `commonLabels`, `labels[].pairs`, `metadata`, and
  inline patches. `helmCharts` and `HelmChartInflationGenerator` are refused;
- the objects a kustomization would produce are counted through its overlays and bases, so a
  "diamond" of overlays that multiplies them is refused before kustomize builds it.

While rendering, kustomize runs with plugins disabled and load restrictions on; Helm template
functions are bounded: a function result over 16 MiB or a list over 100,000 items fails the
render (a template that doubles a string in a loop stops there), and `repeat`, `indent`,
`nindent`, `replace`, `until`, `untilStep` and `seq` are refused before they would build one.
Objects are counted as they are produced: at most 5,000 objects and 16 MiB of output. The Job's
memory limit and deadline bound everything else. A render that cannot succeed fails the step.

## Drift

`.kardinal/rendered.yaml` records the Pipeline, its namespace, the environment, the DRY commit,
the Bundle and the sha256 of every file the last render wrote. Before writing, `render-manifests`
compares the branch with it:

- a rendered branch whose marker names another Pipeline, namespace or environment is refused for
  good: `rendered branch env/prod is not this environment's: it was rendered for Pipeline
  team-a/web environment prod; set render.branch to a branch of its own`. Two Pipelines (in any
  namespace) that render to the same branch of one repository also get `Ready=False` with reason
  `RenderedBranchConflict` on the newer one, before a Bundle reaches it (a Pipeline of another
  namespace is not named, in either message);
- drift is a file kardinal wrote that was edited or deleted, any YAML or JSON file anywhere on the
  branch that kardinal did not write (Argo CD or Flux would apply it with the render), or a branch
  with files but no marker (one kardinal did not write);
- the marker itself is anchored: the RenderRun passes the render Job the marker digests of the
  environment's last 50 successful renders (`status.knownMarkerDigests`), and a marker that is not
  one of them is drift, so a push that edits a file and rewrites the marker to match is caught
  too. A finished Bundle's RenderRuns are deleted with its Graph when the Graph is retired
  ([Graph retirement](concepts.md#graph-retirement)); its steps keep their render's
  marker digest in the Bundle's `status.retiredSteps`, which later renders read too. An
  environment whose earlier Bundles are all gone (deleted, or pruned by `spec.historyLimit`) has
  none, and its branch's marker is used as it is.

What happens on drift:

- `render.onDrift: fail` (default) fails the step with
  `rendered branch env/prod was changed outside kardinal: <file> changed`, and nothing is pushed.
  Restore the branch, or set `onDrift: overwrite`.
- `render.onDrift: overwrite` renders over the change and deletes every YAML or JSON file the
  render does not produce; the RenderRun result and `status.outputs.driftOverwritten` say what was
  overwritten.

Other files (a `CODEOWNERS`, a `README.md`) are not drift and are kept. Files the previous render
wrote and this one does not are deleted.

## Argo CD configuration

Argo CD Applications track the rendered branch:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: my-app-prod
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/myorg/gitops-repo
    targetRevision: env/prod    # rendered branch, not main
    path: .                     # plain YAML at the root (Argo CD's directory source)
  destination:
    server: https://kubernetes.default.svc
    namespace: my-app-prod
  syncPolicy:
    automated: {prune: true, selfHeal: true}
```

The marker is in `.kardinal/`, which a directory source does not read (it does not recurse by
default). With Flux, point a `GitRepository` at `env/prod` and a `Kustomization` at `./`.

## Branch protection

Protect `env/*`: require pull request reviews (CODEOWNERS), and allow direct pushes only from the
Pipeline's git token (`spec.git.secretRef`, which the render Job pushes with) for `approval: auto` environments. kardinal never bypasses branch protection; a
promotion PR waits for the review and the merge like any other.

## Comparison: directory layout vs branch layout

| Aspect | Directory layout (default) | Branch layout (rendered) |
|---|---|---|
| PR diff | Template source diff (`values.yaml`) | Rendered YAML diff |
| Argo CD source type | Kustomize or Helm | Directory (plain YAML) |
| Argo CD performance | Template runs on every reconcile | No template run |
| CODEOWNERS granularity | Overlay directory | Individual rendered files |
| Git history | All environments on one branch | Each environment has its own history, with DRY commit trailers |
| Rollback | Restores the old image over the current source | Re-renders the old DRY commit |
| Where templates run | In the GitOps agent | In a sandboxed render Job per promotion |
| Complexity | Lower | Higher |

## Anti-pattern: do not use `targetRevision` updates

Updating an Argo CD Application's `spec.source.targetRevision` to a new commit, instead of
committing, breaks the GitOps guarantee that Git is the source of truth. With the `kustomize` and
`helm` strategies, and with `layout: branch`, kardinal only writes to Git. The exception is
`update.strategy: argocd`, which patches the Application's `spec.source.helm.valuesObject`
(see [Argo CD native promotion](argocd-native-promotion.md)).

## Examples

`examples/rendered-manifests/` holds a Pipeline and an Argo CD Application for this layout.
