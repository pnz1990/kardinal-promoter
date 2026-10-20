---
section: Added
---

- **`kardinal quickstart --local`: the quickstart with no GitHub account, fork or token** (#1785) — the demo's git repository lives on a git server the quickstart runs in the cluster: one Forgejo pod in `kardinal-git`, reachable only inside the cluster (a ClusterIP Service, nothing published on your machine), seeded with kardinal-demo's environments. The controller and the Pipeline each get a token of its user, written straight into their Secrets and never printed or written to a file. `kardinal quickstart --local --delete` lists, then removes, the demo, the git server, and each of kardinal, Argo CD and kro that the quickstart installed and nothing else uses; only what it marked with its install ID. The quickstart page now starts with it; your GitHub fork still works with `--repo` ([Quickstart](quickstart.md)).
