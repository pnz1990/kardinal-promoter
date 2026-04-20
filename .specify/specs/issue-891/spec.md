# Spec: fix(ci): Demo E2E fails — Pipeline CRD not registered in demo cluster

## Design reference
- N/A — infrastructure fix, no user-visible behavior change

## Zone 1 — Obligations

**O1** — After the fix, `kubectl apply -f demo/manifests/flux/pipeline.yaml` MUST be executed with the `kind-kardinal-control` kubeconfig context active (not `kind-kardinal-dev`). Violation: `demo/scripts/setup.sh` applies the flux pipeline.yaml while the context is still set to the dev cluster.

**O2** — After the fix, `demo/scripts/setup.sh` MUST wait for the Pipeline CRD to be established before applying any Pipeline resources. The wait command: `kubectl wait --for=condition=established crd/pipelines.kardinal.io --timeout=60s`. Violation: any Pipeline resource applied before this wait completes causes "no matches for kind Pipeline" errors.

**O3** — All other Flux resources (`kustomizations.yaml`) MUST continue to be applied on the dev cluster. Only `pipeline.yaml` moves to the control cluster context.

**O4** — The fix MUST be idempotent: running setup.sh twice must not fail.

## Zone 2 — Implementer's judgment

- Whether to use `--ignore-not-found=true` on the wait command (not needed — CRD is always present after Helm install)
- The exact placement of the wait step (before Step 6 is correct per the issue description)

## Zone 3 — Scoped out

- Changes to validate.sh or teardown.sh
- EKS path modifications
- CRD installation changes
