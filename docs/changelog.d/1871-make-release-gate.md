---
section: Development
---

- **`make release-gate`** (#1871) — one command gathers a release's evidence and writes one report: every live suite on the oldest supported and the newest Kubernetes (on this host or in one `e2e-live.yml` dispatch), the upgrade from the previous release, the scale and soak profiles, `go test -race -count=3`, every fuzz target, the scenario and enterprise suites, the static checks, the chart profiles and envtest. It is GO only when everything is proven: a step passes with its log and enough tests recorded, no failure and no more skips than it allows, and every covered `test/e2e/coverage.tsv` row ran and passed. A pass is reused only for the same commit, a clean tree and the same inputs. It takes the host's heavy-load lock and cluster slots by default. See CONTRIBUTING.md, Release gate.
