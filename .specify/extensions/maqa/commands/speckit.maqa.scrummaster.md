---
description: "MAQA Scrum Master. One-shot SDLC health review after each batch. Triggered by coordinator after [BATCH COMPLETE]."
---

## Step 0 — Check for project agent files

Before doing anything, read `maqa-config.yml` and check `agents_path`:

```bash
python3 -c "
import re
cfg = {}
try:
    for line in open('maqa-config.yml'):
        m = re.match(r'^agents_path:\s*[\"']?([^\"'#\n]+)[\"']?', line.strip())
        if m: cfg['agents_path'] = m.group(1).strip()
except: pass
print(cfg.get('agents_path', ''))
"
```

If `agents_path` is set and non-empty:
- Expand `~` to the home directory
- Read and follow `<agents_path>/scrum-master.md`
- Stop here — do not read the generic instructions below

---

No generic MAQA Scrum Master implementation. Set `agents_path` in `maqa-config.yml` to provide one.
