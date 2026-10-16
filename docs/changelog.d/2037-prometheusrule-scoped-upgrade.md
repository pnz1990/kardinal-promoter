---
section: Upgrade
---

- **The chart's alerts carry other labels** (#2037) — if Alertmanager routes, inhibits or silences on these alerts' labels, check them: `KardinalHighReconcileErrors` gains `namespace` (and `cluster` where your series have it) besides `controller`; `KardinalBundleReconcilerStalled`, which had none, gains `namespace` (and `cluster`); `KardinalPolicyGateReconcileSlow` is one alert per installation with `cluster`, `namespace` and `controller`, instead of one per scraped series with its `job`, `instance` and `pod`. `KardinalWorkQueueBacklog` and `KardinalControllerDown` keep their labels. `severity` and the alert names are unchanged ([Monitoring](guides/monitoring.md#prometheus-operator)).
