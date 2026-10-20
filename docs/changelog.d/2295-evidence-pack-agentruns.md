---
section: Added
---

- **The evidence pack holds the AgentRuns its Verdicts name** (#2295). `kardinal audit evidence-pack` writes `agentruns.ndjson`: for each Verdict an in-cluster verifier wrote (`spec.run`), the AgentRun that wrote it, whose status names the agent and its sponsor. Such a Verdict has no actor stamp, so the pack's raw records now name its agent without the cluster ([Trace a Verdict to its agent](compliance.md#trace-a-verdict-to-its-agent)). The AgentRuns are paged and redacted like every other record. The chart's `<release>-auditor` role gains `get` and `list` on `agentruns`; an exporter without them gets the file left out and `AgentRun` under `coverage.forbidden`. A Verdict whose AgentRun the cluster no longer holds is counted in a warning. `controls.json` cites the file on the change-testing rows and on EU AI Act Article 50.
