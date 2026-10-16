---
section: Upgrade
---

- **Requires Kubernetes 1.35 or later** (#1876) — the chart's `kubeVersion` and `hack/install-kro.sh` refuse an older cluster: check `kubectl version` before you upgrade ([Upgrade](upgrade.md)). `kardinal` reads one Pipeline's Bundles only through the Bundle CRD's selectable field, with no namespace-wide fallback.
