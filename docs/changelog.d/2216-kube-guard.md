---
section: Development
---

- **Unit tests never reach the cluster your machine points at** (#2216) — every test package under `cmd/` and `test/`, and every other one that starts processes or whose tests can reach a kube config loader, runs under `kubetest.Main` (`test/kubetest`): before any test, `KUBECONFIG` points at a path that does not exist, `$HOME` at an empty directory and client-go's home kubeconfig at nothing, and `KUBERNETES_SERVICE_HOST` and `USE_EXISTING_CLUSTER` are unset, for the tests and every process they start. `make hygiene` fails on a package without the guard. The e2e suites, which talk to their own kind clusters, are not checked ([CONTRIBUTING.md](https://github.com/pnz1990/kardinal-promoter/blob/main/CONTRIBUTING.md)).
