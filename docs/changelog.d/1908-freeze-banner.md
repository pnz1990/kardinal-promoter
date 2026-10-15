---
section: Changed
---

- **Freeze banners, the same as every page-wide notice** (#1908) — each active freeze is a banner on every view saying whom it holds and where, who froze (the actor badge), since and until when, and why; stop-the-line is in the failed colour. Without the right to lift it, or with Helm `ui.freezeControls` off, the banner names `kardinal unfreeze` with the reason. The stage drawer of an environment a freeze holds says so. A banner that arrives with the page's first read no longer pushes the page down. The freeze, rejected-change-live and plain-HTTP banners share one Banner component. See [Freezes in the UI](agentic/freezes.md#in-the-ui).
