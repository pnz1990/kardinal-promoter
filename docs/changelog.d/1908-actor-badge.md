---
section: Added
---

- **Who shipped a version, the same everywhere** (#1908) — one actor badge (a person, an agent with its sponsor, a bot, the controller; a glyph and words, never color alone) in the stage drawer, approvals, the version strip, the fleet board and the operations table. A version an agent shipped is marked on the map (`· agent`) and the fleet board, and the new **Agents** filter (`filter=agents`) keeps the pipelines an agent shipped to. The UI API's pipelines carry `activeBundleActor` and `deployed[env].actor`. An `/env/` link takes the drawer's column from its first render. See [Who shipped a version](ui.md#who-shipped-a-version)
