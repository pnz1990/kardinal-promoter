---
section: Development
---

- **Unit tests get no credentials and no late guard** (#2321) — the kube guard (`test/kubetest`) now gives every guarded test package a global gitconfig with only a commit identity (`GIT_CONFIG_GLOBAL`, and no system config) and an empty `DOCKER_CONFIG`, so a test's `git push` or registry read reaches none of your credential helpers or logins; a test that needs either writes its own. `make hygiene` refuses a guard that runs after the tests start (through a helper, a `defer` or an `if`), a read of the home directory at package initialization, and a child process started with an environment built from scratch and no `HOME`, which a controller-runtime child would turn into the login account's `~/.kube/config`. A child test binary handed a home (`kubetest.HomeEnv`) uses it, so the controller's `-help` test leaves no `/tmp/kardinal-unit-home-*` behind ([CONTRIBUTING.md](https://github.com/pnz1990/kardinal-promoter/blob/main/CONTRIBUTING.md)).
