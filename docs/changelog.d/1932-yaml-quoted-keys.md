---
section: Added
---

- **Quoted keys in YAML paths** (#1932). `update.yaml.updates[].path` and `update.helm.chartVersionPath` can now address a mapping key that contains `.` or `/` by quoting it: `metadata.annotations["app.oam.dev/publishVersion"]`, `metadata.labels["app.kubernetes.io/version"]`. A Pipeline that promotes a KubeVela `application.yaml` can now bump `publishVersion` (with the image tag, for example) in the same commit as the image, so KubeVela re-renders the workload. A quoted key is always a mapping key, and it is created when missing like any other key. The other rules are unchanged: one document, no anchors, a scalar target. The CRD patterns of both fields accept the new form, so apply the new CRDs (`helm upgrade`, or `config/crd/bases`) before you use it. See [YAML paths](pipeline-reference.md#yaml-paths).
