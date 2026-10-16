---
section: Fixed
---

- **Long Subscription, MetricCheck and AnalysisTemplate names no longer break their objects** (#2034) — their names are copied into labels, and a name longer than 63 characters (Kubernetes names can be 253) made the API server refuse the write: a Subscription made no Bundle, and a per-promotion MetricCheck or an analysis made the whole Graph fail. Such a name is now written to its label as its first 52 characters and a 10-character hash of the whole name; shorter names are unchanged
