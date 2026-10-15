---
section: Changed
---

- **A Graph cleanup no longer reads every Graph of the namespace** (#1685) — to find the reader RoleBindings no Graph needs any more, each Graph deletion read every kardinal Graph of the namespace from the API server, each a full kro spec: in the scale soak (about 178 Graphs live) that allocated 119 GB, 17% of the controller's allocations. The controller now labels each Graph with the namespaces it reads (`reads.kardinal.io/<namespace>: "true"` and `kardinal.io/reader-labels: "true"`), and a prune asks the API server for the metadata of at most one Graph per namespace it may unbind. A namespace that still holds a Graph from an earlier release is read in full, as before, until that Graph goes.
