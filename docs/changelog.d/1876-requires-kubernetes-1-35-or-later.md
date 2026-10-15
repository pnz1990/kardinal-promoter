---
section: Changed
---

- **Requires Kubernetes 1.35 or later** (#1876) — the chart's `kubeVersion` and `hack/install-kro.sh` refuse an older cluster: check `kubectl version` before you upgrade ([Upgrade](upgrade.md)). The live e2e suites, the upgrade suites included, run on 1.35, 1.36 and 1.37, and `make envtest` installs the CRDs and admission policies on 1.35 and the newest release. `kardinal` reads one Pipeline's Bundles only through the Bundle CRD's selectable field, with no namespace-wide fallback
