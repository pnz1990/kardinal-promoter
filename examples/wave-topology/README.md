# Wave Topology — Multi-Region Production Deployment

This example demonstrates the `wave:` field for parallel multi-region production
rollouts. Environments with the same wave number are promoted simultaneously; each
wave waits for all environments in the previous wave to reach Verified.

Pipeline topology:
  test (no wave) → staging (no wave) → [prod-eu, prod-us] (wave 1) → prod-ap (wave 2)

The Pipeline uses the placeholder repo `https://github.com/myorg/gitops-repo`. Change
`spec.git.url` to a GitOps repo with an `environments/<name>` Kustomize overlay for each
of the five environments, then apply with:
  kubectl apply -f examples/wave-topology/pipeline.yaml

See docs/pipeline-reference.md for full wave: field documentation.
