---
description: "Bounded standalone agent. Reads scope from BOUNDARY file. Multiple sessions run concurrently on different areas without conflicts."
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
