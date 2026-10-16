---
section: Added
---

- **PipelinePaused and PipelineResumed AuditEvents carry `kardinal.io/pipeline-generation`**: the label is the generation of the Pipeline change the event records. Two pauses of one Pipeline can now be told apart from one pause recorded twice (the same generation) in queries and in the e2e audit invariant.
