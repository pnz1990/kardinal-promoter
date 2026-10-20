---
section: Changed
---

- **The CLI pages through large reads three times faster** (#2047) — it no longer uses client-go's default client rate limit of 5 requests a second (bursts of 10), which throttled every paged read: `kardinal audit evidence-pack` of 100,000 AuditEvents, 200 pages of 500, took 39.6 s and now takes 12.5 s on a kind cluster. The CLI's limit is now 50 requests a second with bursts of 300; the API server's priority and fairness still protects it. `KARDINAL_KUBE_QPS` and `KARDINAL_KUBE_BURST` set other numbers, for a small or busy control plane ([CLI reference](reference/cli/kardinal.md)).
