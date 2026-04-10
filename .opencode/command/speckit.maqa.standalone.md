---
description: "Single-session standalone agent. Plays all roles sequentially: coordinator → engineer → QA (adversarial) → SM → PM → repeat. Fully autonomous, one item at a time."
---

```bash
AGENTS_PATH=$(python3 -c "
import re, os
for line in open('maqa-config.yml'):
    m = re.match(r'^agents_path:\s*[\"\'']?([^\"\'#\n]+)[\"\'']?', line.strip())
    if m: print(os.path.expanduser(m.group(1).strip())); break
" 2>/dev/null)
```

Read and follow `$AGENTS_PATH/standalone.md`.
