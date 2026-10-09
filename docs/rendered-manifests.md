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

The render is offline: subcharts must be vendored in `charts/`, `lookup` finds nothing, and the
default capabilities are used. Hooks and `NOTES.txt` are not rendered.

## What a promotion does

| Step | What it does |
|---|---|
| `git-clone` | Clones `env/<name>` (the first time it creates the branch, with only the render marker) and checks out the DRY source next to it |
| `kustomize-set-image` | Edits the image in the DRY overlay (`helm-set-image` for a chart). The DRY source is never committed to |
| `render-manifests` | Renders the environment path in the controller, checks `env/<name>` for drift, writes one file per object and the render marker |
| `git-commit` | Commits to `env/<name>`, with the DRY commit in the message trailers |
| `git-push` | Pushes `env/<name>`, or `kardinal/<bundle>/<env>` for a PR |
| `open-pr`, `wait-for-merge` | For `approval: pr-review`, a PR into `env/<name>` whose diff is the rendered YAML |
| `health-check` | The Argo CD Application or Flux Kustomization that syncs `env/<name>` |

The rendered commit's message ends with:

```
Kardinal-Dry-Commit: 4f2c...
Kardinal-Dry-Path: environments/prod
Kardinal-Bundle: my-app-v1-29-0
```

### Which DRY commit is rendered

- A Bundle with `configRef.commitSHA` (a config or mixed Bundle, or an image Bundle that pins its
  DRY commit) renders that commit. `configRef.gitRepo` must be empty or the Pipeline's repository.
- A rollback Bundle renders the DRY commit its target was rendered from in this environment. It
  is found from the `Kardinal-Bundle` and `Kardinal-Dry-Commit` trailers in the last 500 commits of
  the rendered branch; if the target's render is not there, the step fails (pin the commit with
  `configRef.commitSHA`). `kardinal rollback` therefore restores the old manifests, not the old
  image over today's DRY source.
- Otherwise the head of `spec.git.branch` is rendered. The step records the commit in
  `status.outputs.dryCommit`.

### Determinism and limits

kustomize (`sigs.k8s.io/kustomize/api`) and Helm (`helm.sh/helm/v4`) run inside the controller at
the versions in its `go.mod`. The image ships no `kustomize` or `helm` binary, and a render never
runs a command. kustomize runs with plugins disabled and load restrictions on. Remote resources
(`https://...`, `github.com/...?ref=`), `helmCharts` in a kustomization, and symbolic links in the
DRY source are refused. Limits: 64 MiB and 20,000 files of DRY source, 16 MiB and 5,000 objects
of output, and 60 seconds per render. A render that cannot succeed (a kustomize or template error,
a limit) fails the step without retries.

## Drift

`.kardinal/rendered.yaml` records the DRY commit, the Bundle and the sha256 of every file the last
render wrote. Before writing, `render-manifests` compares the branch with it. Drift is a file
kardinal wrote that was edited or deleted, a file someone added where a new rendered file goes, or
a branch with files but no marker (one kardinal did not write):

- `render.onDrift: fail` (default) fails the step with
  `rendered branch env/prod was changed outside kardinal: <file> changed`, and nothing is pushed.
  Restore the branch, or set `onDrift: overwrite`.
- `render.onDrift: overwrite` renders over the change, and the step message and
  `status.outputs.driftOverwritten` say what was overwritten.

Files kardinal never wrote (a `CODEOWNERS`, a `README`) are not drift and are kept. Files the
previous render wrote and this one does not are deleted.

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
kardinal token for `approval: auto` environments. kardinal never bypasses branch protection; a
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
| Complexity | Lower | Higher |

## Anti-pattern: do not use `targetRevision` updates

Updating an Argo CD Application's `spec.source.targetRevision` to a new commit, instead of
committing, breaks the GitOps guarantee that Git is the source of truth. With the `kustomize` and
`helm` strategies, and with `layout: branch`, kardinal only writes to Git. The exception is
`update.strategy: argocd`, which patches the Application's `spec.source.helm.valuesObject`
(see [Argo CD native promotion](argocd-native-promotion.md)).

## Examples

`examples/rendered-manifests/` holds a Pipeline and an Argo CD Application for this layout.
