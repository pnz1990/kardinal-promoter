---
section: Added
---

- **The UI under a path prefix** (#1933). With `ui.basePath` (`--ui-base-path`), for example `/kardinal`, the controller also serves the UI at `/kardinal/ui/` and its API at `/kardinal/api/v1/ui/`, so one Ingress rule on a shared host routes it by path, without rewriting it. The UI is built with relative asset URLs and finds its API from its own address; authentication, host and CORS checks are the same under the prefix, and `/ui/` keeps working for `kubectl port-forward`. [Under a path prefix](installation.md#under-a-path-prefix-on-a-shared-host).
