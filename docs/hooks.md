# Pre- and Post-Deploy Hooks

A hook is a Kubernetes Job that kardinal runs once per Bundle in an environment:

- a **pre** hook runs before the environment's promotion starts, for example a database
  migration. The promotion starts only after every pre hook succeeded;
- a **post** hook runs after the environment's health check passed, for example an
  integration test against the new version. The environment is Verified, and the next
  environment can start, only after every post hook succeeded.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
spec:
  git:
    url: https://github.com/myorg/gitops-repo
  environments:
    - name: test
    - name: prod
      hooks:
        - name: migrate
          phase: pre
          timeout: 15m
          job:
            backoffLimit: 0
            template:
              spec:
                serviceAccountName: migrator      # must be in hooks.serviceAccounts
                containers:
                  - name: migrate
                    image: ghcr.io/myorg/my-app-migrations:1.4.0
                    args: ["up"]
                    envFrom:
                      - secretRef: {name: prod-db}
        - name: smoke
          phase: post
          job:
            backoffLimit: 1
            template:
              spec:
                containers:
                  - name: smoke
                    image: curlimages/curl:8.10.1
                    command: ["sh", "-c", "curl -fsS http://my-app.prod:8080/healthz"]
```

## Fields

`spec.environments[].hooks[]`, at most 10 per environment:

| Field | Required | Default | Description |
|---|---|---|---|
| `name` | Yes | | DNS label, at most 40 characters, unique within the environment. The HookRun and its Job are named `<pipeline>-<bundle>-<environment>-<phase>-<name>`, hash-suffixed when that is longer than 63 characters. |
| `phase` | Yes | | `pre` or `post`. |
| `job` | Yes | | A `batch/v1` [JobSpec](https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/job-v1/#JobSpec). The CRD keeps the field untyped; the controller rejects a job that is not a valid JobSpec (unknown fields included) or has no containers when it builds the Bundle's Graph, and the Bundle fails with `TranslationError`. `template.spec.restartPolicy` defaults to `Never` and `activeDeadlineSeconds` to the timeout. |
| `timeout` | No | `30m` | Go duration from the moment the hook starts. A hook still running then fails and its Job is deleted, with its Pods. |

Strings in the job are passed to the Pod as written: `${HOME}` in a command is expanded by
the shell in the container, not by kardinal or kro.

## How a hook runs

1. The Bundle's Graph creates a `HookRun` (`kubectl get hookruns`) for each hook. A pre
   hook's HookRun is created when the environment could start: its upstream environments are
   Verified, its PolicyGates are ready and the Bundle is not Superseded. A post hook's HookRun
   is created when the step enters `Verifying`. Hooks of one phase run one after another, in
   the order they are listed: each waits for the one before it to succeed.
2. The HookRun controller creates the Job, named after the HookRun, in the Pipeline's
   namespace, with the HookRun as its owner. It records the Job's result in
   `HookRun.status.phase`: `Pending`, `Running`, then `Succeeded` or `Failed`.
3. The Graph copies each HookRun's result onto the environment's PromotionStep
   (`spec.live.hooks`), and the step acts on it:
    - while a pre hook runs, the step stays `Pending` with the message
      `waiting for pre-deploy hook <name> (HookRun <run>): Running`. Nothing is committed or
      deployed. A failed pre hook fails the step (`pre-deploy hook <name> (HookRun <run>)
      failed: ...`) and the Bundle;
    - after the health check (and any `bake` window) passed, a step with post hooks is
      `Verifying` until they finish. All succeeded: `Verified`. One failed: the environment's
      `onHealthFailure` applies, as for a failed health check (`none` fails the step, `abort`
      stops it for a human, `rollback` rolls the environment back). The change is already
      deployed when a post hook runs; a failed post hook does not revert it by itself.

```
Pending ──pre hooks succeeded──▶ Promoting ─▶ (WaitingForMerge) ─▶ HealthChecking
                                                                      │ healthy
                                                                      ▼
                                       Verified ◀──post hooks succeeded── Verifying
```

## Guarantees

- **A hook runs once per Bundle and environment.** A finished HookRun never creates its Job
  again: deleting the Job (by hand, a TTL, a cleanup tool) after it finished changes nothing.
  A Job deleted, or replaced by another Job of that name, while it runs fails the HookRun; the
  hook is not started a second time. Re-run a hook by promoting a new Bundle.
- **Pipeline edits do not change a running hook.** An edit to a hook while it runs reaches
  the HookRun's spec, but the running Job keeps the spec it started with; the HookRun gets the
  condition `SpecChangedAfterStart`. The next Bundle runs the edited hook.
- **Nothing is left running.** Deleting the Bundle deletes its Graph, its HookRuns, their Jobs
  and Pods (garbage collection through the owner references).
- **A superseded Bundle starts no hooks.** A hook that is already running finishes; the
  superseded Bundle's step does not start, and the new Bundle runs its own hooks.

## Security: which ServiceAccount a hook runs as

The Pod's `serviceAccountName` (empty means `default`) must be one of the controller's
`--hook-service-accounts` (Helm `hooks.serviceAccounts`, default `[default]`), in the
Pipeline's namespace. A hook with any other ServiceAccount fails without a Job and so does
its step. The Graph ServiceAccount (`graph.serviceAccountName`, `kardinal-graph`) is never
allowed, whatever the list says: it can write every object a Graph renders.

Anyone who can edit a Pipeline can run a Pod in its namespace as one of these
ServiceAccounts. List only ServiceAccounts that hold what the hooks need, and enforce Pod
Security Admission on the namespace as for any workload. The controller needs `create`,
`get`, `list`, `watch` and `delete` on `batch/jobs` in Pipeline namespaces (the chart grants
it) and caches only Jobs labelled `kardinal.io/hookrun`.

## What hooks cannot do

- Hooks run in the Pipeline's namespace in the cluster kardinal runs in, not in the target
  cluster of a remote environment. Reach the target through its Service or API from the Pod.
- A pre hook runs before the step starts. When a PolicyGate turns false after the migration
  ran and before the step started, the step waits for the gate with the migration already
  applied: write migrations that the running version tolerates (expand, then contract).
- Hook results reach the step through the Graph (kro). While kro is down the step waits.
- A rollback Bundle is a Bundle: it runs the hooks of every environment it promotes through.

## Inspecting hooks

```bash
kubectl get hookruns -n my-app
# NAME                                    PIPELINE  ENV   HOOK     WHEN  PHASE      AGE
# my-app-my-app-x7k2p-prod-pre-migrate    my-app    prod  migrate  pre   Succeeded  6m
kubectl logs -n my-app job/my-app-my-app-x7k2p-prod-pre-migrate
kubectl get promotionstep -n my-app my-app-my-app-x7k2p-prod -o jsonpath='{.spec.live.hooks}'
```

## Design

`HookRun` is an owned node: the Graph creates it, and its own controller runs the Job and
writes `status`. A `batch/v1` Job node in the Graph does not work: kro deletes and prunes
without a propagation policy, so a Job's Pods outlive it; kro re-creates a deleted Job, which
runs the hook again; and an edited Job template is an immutable-field error that stops the
whole Graph. The results reach the PromotionStep through a `patch` node whose target is the
step's literal name, so they keep arriving after the step's own template stopped resolving.
See ledger entries [G12](design/16-graph-capability-ledger.md#g12-delete-and-prune-orphan-the-pods-of-a-job)
and [G14](design/16-graph-capability-ledger.md#g14-a-node-with-one-pending-field-is-wholly-unresolved),
and [Graph coverage](graph-coverage.md).
