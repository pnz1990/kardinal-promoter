# Example: Minimal Pipeline

The smallest Pipeline that promotes: 12 lines, everything else defaulted.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata: { name: my-app }
spec:
  git:
    url: https://github.com/myorg/gitops
    secretRef: { name: github-token }
  environments:
    - name: test
    - name: uat
    - name: prod
      approval: pr-review
```

What the defaults fill in:

| Field | Default |
|---|---|
| Order | the list order: uat after test, prod after uat |
| `git.branch` | `main` |
| `git.layout` | `directory` |
| `environments[].path` | `environments/<name>` |
| `environments[].approval` | `auto` (prod sets `pr-review`: a PR waits for your merge) |
| `environments[].update.strategy` | `kustomize` (sets the image in `environments/<name>/kustomization.yaml`) |
| `environments[].health` | `resource`: Deployment `my-app` in namespace `<name>` is Available, within `10m` |
| `historyLimit` | `50` |

## Use it

1. Replace `https://github.com/myorg/gitops` with your GitOps repository, which has a
   Kustomize overlay at `environments/test`, `environments/uat` and `environments/prod`,
   each deploying Deployment `my-app` into the namespace of the environment's name.
2. Create the token Secret the Pipeline names, in the Pipeline's namespace, and label it
   so a Pipeline may use it:

   ```bash
   kubectl create secret generic github-token --from-literal=token="$GITHUB_PAT"
   kubectl label secret github-token kardinal.io/referenceable=true
   ```

3. Check and apply the Pipeline, then promote an image:

   ```bash
   kardinal validate -f pipeline.yaml
   kubectl apply -f pipeline.yaml
   kardinal create bundle my-app --image ghcr.io/myorg/my-app:1.2.3
   kubectl get pipelines
   ```

test and uat update on their own; prod opens a PR. Each field you need beyond these is
in the [Pipeline reference](../../docs/pipeline-reference.md); the
[quickstart](../quickstart/) adds Argo CD health checks and policy gates.
