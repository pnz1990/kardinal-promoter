---
section: Upgrade
---

- **New length limits on free text** (#1867) — Bundles' `spec.provenance.author` (256 characters) and `ciRunURL` (2048), PolicyGate overrides' `reason` (1024) and `stage` (63), and ChangeWindows' `reason` (1024) now have limits. Objects stored over a limit before the upgrade stay writable while that field is unchanged, because Kubernetes 1.35+ ratchets CRD validation. Changing the over-long field is refused. A gate that holds an over-long override takes no new override until that entry is removed, because the overrides list is atomic and every entry is checked again; the gate's status keeps the override's audit record.
