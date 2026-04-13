# Wave Topology — Multi-Region Production Deployment

This example demonstrates the `wave:` field for parallel multi-region production
rollouts. Environments with the same wave number are promoted simultaneously; each
wave waits for all environments in the previous wave to reach Verified.

Pipeline topology:
  test (no wave) → staging (no wave) → [prod-eu, prod-us] (wave 1) → prod-ap (wave 2)

Apply with:
  kubectl apply -f examples/wave-topology/pipeline.yaml

See docs/pipeline-reference.md for full wave: field documentation.
