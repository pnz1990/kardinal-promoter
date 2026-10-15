---
section: Fixed
---

- **Notification events carry when they happened** (#1945) — a NotificationHook's `Bundle.Verified`, `Bundle.Failed`, `Bundle.Superseded`, `Bundle.RolledBack`, `PromotionStep.Failed`, `PromotionStep.PROpened` and `PromotionStep.WaitingForApproval` events had the Bundle's or the step's creation time as their CloudEvents `time`, and were sent in that order. They now carry the time of the change: the new `status.phaseTransitionTime` of a Bundle and `status.stateTransitionTime` of a PromotionStep, and for `PROpened` when the `open-pr` step finished ([Notifications](notifications.md)). A retry keeps the time. A hook created after a version was shipped but before it was Verified now sends its `Bundle.Verified`.
