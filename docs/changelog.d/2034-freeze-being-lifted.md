---
section: Fixed
---

- **A freeze being lifted still counted, and a long freeze name kept its audit from being written** (#2034) — after `kardinal unfreeze`, the freeze's audit finalizer keeps the window for up to 2 minutes. Meanwhile `kardinal override --freeze` could add its exemption to it, `kardinal freeze`, the UI banner and the MCP `freezes` tool listed it, and a gate's `changewindow.<name>` was still true. A freeze marked lifted or being deleted now counts for none of them. A freeze named after a long namespace (`kardinal-freeze-agents-<namespace>`, more than 63 characters) had every `FreezeStarted` and `FreezeLifted` record refused for its label value; the label now carries a short, hashed form of the name ([Freezes](agentic/freezes.md#who-froze-and-who-lifted-it))
