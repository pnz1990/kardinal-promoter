"""
docs/gen_ref_pages.py — Auto-generates reference pages from code.

This script runs during mkdocs build via the gen-files plugin.
It does NOT create new content — it wires existing auto-generated
files (CLI docs from Cobra, API docs from gen-crd-api-reference-docs)
into the nav without duplicating anything.
"""

import mkdocs_gen_files
from pathlib import Path

# Wire the auto-generated CLI reference pages (produced by hack/gen-cli-docs/main.go)
# into the reference/cli/ section. If not yet generated, creates a placeholder.
cli_dir = Path("docs/reference/cli")
if cli_dir.exists():
    for path in sorted(cli_dir.glob("*.md")):
        # Already in the right place — gen-files just ensures they're included
        pass
else:
    with mkdocs_gen_files.open("reference/cli.md", "w") as f:
        f.write("""# CLI Reference

!!! note "Auto-generated"
    This page is auto-generated from the kardinal CLI source code.
    Run `go run ./hack/gen-cli-docs/main.go` to regenerate locally.
    
    The full CLI reference will appear here after the first CI run.

## Available Commands

| Command | Description |
|---|---|
| `kardinal version` | Show CLI and controller versions |
| `kardinal get pipelines` | List all promotion pipelines |
| `kardinal get bundles` | List bundles for a pipeline |
| `kardinal get steps` | Show promotion steps for a bundle |
| `kardinal explain` | Show PolicyGate details for an environment |
| `kardinal create bundle` | Create a new Bundle to promote |
| `kardinal policy simulate` | Simulate PolicyGate evaluation |
| `kardinal policy test` | Test a CEL expression |
| `kardinal policy list` | List active PolicyGate instances |
| `kardinal pause` | Pause promotion for a pipeline |
| `kardinal resume` | Resume promotion for a pipeline |
| `kardinal rollback` | Roll back an environment to a previous bundle |
| `kardinal approve` | Manually approve a Bundle for an environment |
""")
