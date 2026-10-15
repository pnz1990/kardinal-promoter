---
section: Fixed
---

- **The admin ClusterRole no longer shows OutOfSync in Argo CD** (#1912) — `<release>-admin` is aggregated, so the chart no longer renders `rules: []` on it; the API server writes its rules. Applications that ignore `/rules` of that role as a workaround can drop the `ignoreDifferences` entry.
