---
section: Fixed
---

- **The Grafana dashboard shows only kardinal-promoter in a shared Prometheus** (#1941) — the reconcile and work queue panels summed every controller-runtime operator the Prometheus scrapes (kro, Crossplane, External Secrets, other clusters): in a three-cluster Amazon Managed Prometheus, kardinal was 72 of 13,952 reconcile series. Every panel is now scoped: new `Cluster` and `Namespace` variables (default All) select the controller, and the controller-runtime, work queue and Go runtime panels keep to kardinal's own scrape targets, whatever the scrape job is called ([Grafana Dashboard](guides/monitoring.md#grafana-dashboard)).
