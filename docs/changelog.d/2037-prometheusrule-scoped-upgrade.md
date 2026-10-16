---
section: Upgrade
---

- **The chart's alerts carry other labels** (#2037) — check any Alertmanager route, inhibit rule or silence that matches the labels of these alerts (`prometheusRule.enabled`). The alert names, `severity` and the annotations are unchanged. "Scrape labels" below are the target labels your scrape adds: with the chart's ServiceMonitor, `job`, `instance`, `namespace`, `pod`, `service`, `endpoint` and `container`. `cluster` appears only where your series carry it.

  | Alert | Labels before | Labels after |
  |---|---|---|
  | `KardinalHighReconcileErrors` | `alertname`, `severity`, `controller` | `alertname`, `severity`, `controller`, `namespace` (+ `cluster`) |
  | `KardinalBundleReconcilerStalled` | `alertname`, `severity` | `alertname`, `severity`, `namespace` (+ `cluster`) |
  | `KardinalPolicyGateReconcileSlow` | `alertname`, `severity`, `controller` and the scrape labels: one alert per scraped series, so one per replica | `alertname`, `severity`, `controller`, `namespace` (+ `cluster`): one alert per installation |
  | `KardinalWorkQueueBacklog` | `alertname`, `severity`, `name`, `controller`, scrape labels | unchanged |
  | `KardinalControllerDown` | `alertname`, `severity`, scrape labels | unchanged |

  **What to adjust.**
  - A route, inhibit rule or silence that matches `job`, `instance`, `pod`, `service`, `endpoint` or `container` on `KardinalPolicyGateReconcileSlow` no longer matches it. Match `namespace` instead (the release namespace), and `cluster` if you have more than one.
  - A route that groups these alerts with `group_by: ['...']` (every label) or by `namespace` or `cluster` sees new groups. An alert that is firing during the upgrade is resolved under its old label set and fires again under the new one, so expect one re-notification for each.
  - A silence on `KardinalHighReconcileErrors` or `KardinalBundleReconcilerStalled` that matches on all of the old labels still matches, because it is a subset.
