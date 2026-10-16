---
section: Security
---

- **A YAML file whose aliases expand exponentially no longer holds the controller for minutes** (#2078). The yaml, helm and kustomize update strategies compare each edited file with its original value by value, following its aliases.
  - **Before:** a 460-byte "billion laughs" file in a GitOps repository, with aliases of aliases eight deep (10^8 values), took two minutes of the controller's CPU per promotion attempt. Each extra level made it 10 times longer.
  - **Now:** the comparison visits at most ten times the file's nodes, and at least 2^20 (about a million), well under a second. Values files whose shared blocks are merged or aliased into hundreds of entries stay far within that. A file past it fails the step and is left unchanged, with a message saying why.
