---
description: "Bounded standalone agent. Inject boundary fields directly in your prompt — no files needed. Multiple sessions run concurrently on different areas."
---

```bash
AGENTS_PATH=$(python3 -c "
import re, os
for line in open('maqa-config.yml'):
    m = re.match(r'^agents_path:\s*[\"\'']?([^\"\'#\n]+)[\"\'']?', line.strip())
    if m: print(os.path.expanduser(m.group(1).strip())); break
" 2>/dev/null)
```

Read and follow `$AGENTS_PATH/bounded-standalone.md`.

Inject boundary fields in your prompt after this command (no BOUNDARY file needed):

```
AGENT_ID=STANDALONE-<NAME>
SCOPE=<one sentence>
ALLOWED_AREAS=area/controller,area/cli,...
ALLOWED_MILESTONES=v0.2.1,...
ALLOWED_PACKAGES=pkg/reconciler,...
DENY_PACKAGES=cmd/kardinal,...
```
