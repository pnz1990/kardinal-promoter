---
section: Fixed
---

- **The MetricCheck instances of a Superseded or Rejected version stop querying, and a Rejected version's Graph is retired** (#2138) — the Graph of a Superseded or Rejected version holds its steps, so it never set its instances' `spec.suspend`, and they kept querying the metrics backend until the Graph was retired. A Rejected version's Graph was never retired at all. The controller now stops those instances as soon as the version is Superseded or Rejected, and retires a Rejected version's Graph like a Superseded one's (`graph.retire.superseded`, 1 minute by default) ([MetricChecks](metric-checks.md)).
