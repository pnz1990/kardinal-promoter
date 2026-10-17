---
section: Added
---

- **Version skew warning** (#1757) — when the CLI and the controller are a minor version apart, a command that talks to the cluster prints one `warning: kardinal CLI v0.13.0 talks to controller v0.12.1 (in kardinal-system); some output may not match: ...` line on stderr. The warning includes the command that upgrades the older side: `helm upgrade` with the CLI's chart version, or the `curl` download of the CLI release that matches the controller. The controller version is read from the `kardinal-version` ConfigMap, as `kardinal version` reads it (in `kardinal-system`, or `--controller-namespace` for commands that have that flag). There is no warning for a patch difference, with `-o json` or `yaml`, from `kardinal version` itself, for dev builds, or when the ConfigMap can't be read. See [Install the CLI](installation.md#3-install-the-cli).
