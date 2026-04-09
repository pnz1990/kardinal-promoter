# 01: Graph Integration Layer

> Status: Outline
> Depends on: nothing (foundation)
> Blocks: everything else

This is the foundation. Everything else builds on it.

## Scope

- How to import the experimental Graph library from ellistarn/kro/tree/krocodile/experimental
- Go package structure for the Graph dependency
- Graph spec generation: the template for PromotionStep and PolicyGate nodes
- Dependency edge strategy (upstreamVerified/requiredGates fields vs future dependsOn)
- Testing strategy: how to run the Graph controller in integration tests
- What happens when the Graph API changes (compatibility strategy)
- Graph CRD installation: what CRDs must exist before kardinal-controller starts
- Error handling: what the controller does when Graph controller is unavailable or returns errors
