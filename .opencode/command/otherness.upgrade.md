---
description: "DEV: Check for speckit/maqa/aide/extension updates, show changelog, and apply with confirmation. Run by the otherness maintainer, not customers."
---

You are the otherness dependency upgrade agent. You check for new versions of speckit, maqa, aide, and other extensions that otherness depends on, show what changed, and apply updates.

## Step 1 — Current installed versions

```bash
echo "=== Installed versions ==="
echo "speckit (opencode integration):"
python3 -c "import json; d=json.load(open('.specify/integrations/speckit.manifest.json')); print(f'  v{d[\"version\"]} installed {d[\"installed_at\"][:10]}')" 2>/dev/null || echo "  not found"

echo "opencode integration:"
python3 -c "import json; d=json.load(open('.specify/integrations/opencode.manifest.json')); print(f'  v{d[\"version\"]} installed {d[\"installed_at\"][:10]}')" 2>/dev/null || echo "  not found"

echo "otherness agent files:"
git -C ~/.otherness log --oneline -5 2>/dev/null | sed 's/^/  /' || echo "  not a git repo"

echo "Extensions:"
ls .specify/extensions/ 2>/dev/null | grep -v '^\.' | sed 's/^/  /'
```

## Step 2 — Check community catalog for updates

```bash
echo ""
echo "=== Checking community catalog ==="
CATALOG_URL=$(python3 -c "
import re
for line in open('.specify/extension-catalogs.yml'):
    m = re.match(r'^\s+url:\s*(.+)', line)
    if m: print(m.group(1).strip()); break
" 2>/dev/null || echo "https://raw.githubusercontent.com/github/spec-kit/main/extensions/catalog.community.json")

curl -sL "$CATALOG_URL" 2>/dev/null | python3 - << 'EOF'
import sys, re
content = sys.stdin.read()
# Extract extension names and versions from the catalog YAML
extensions = re.findall(r'^\s+name:\s+(\S+).*?version:\s+(\S+)', content, re.MULTILINE | re.DOTALL)
for ext in ['maqa', 'aide', 'maqa-ci', 'maqa-github-projects', 'verify', 'verify-tasks', 'review', 'ship', 'worktree', 'git']:
    for name, version in extensions:
        if name == ext:
            print(f"  {name}: {version}")
            break
EOF
```

## Step 3 — Check otherness upstream for new commits

```bash
echo ""
echo "=== otherness upstream (pnz1990/otherness) ==="
git -C ~/.otherness fetch --quiet 2>/dev/null

LOCAL=$(git -C ~/.otherness rev-parse HEAD 2>/dev/null)
REMOTE=$(git -C ~/.otherness rev-parse origin/main 2>/dev/null)

if [ "$LOCAL" = "$REMOTE" ]; then
  echo "  Up to date."
else
  echo "  New commits available:"
  git -C ~/.otherness log --oneline "$LOCAL..$REMOTE" 2>/dev/null | sed 's/^/    /'
fi
```

## Step 4 — Check speckit CLI version

```bash
echo ""
echo "=== speckit CLI ==="
specify --version 2>/dev/null || echo "  specify not installed (uv tool install specify-cli)"

# Check PyPI for latest
LATEST=$(curl -s https://pypi.org/pypi/specify-cli/json 2>/dev/null | \
  python3 -c "import json,sys; d=json.load(sys.stdin); print(d['info']['version'])" 2>/dev/null || echo "unknown")
echo "  Latest on PyPI: $LATEST"
```

## Step 5 — Present update plan and confirm

Based on the above findings, produce a summary:

```
=== UPDATE PLAN ===

otherness agent files: <N new commits — titles>
speckit CLI: <current> → <latest>
maqa extension: <current> → <latest>
aide extension: <current> → <latest>
<any other extensions with available updates>

Apply? (yes/no)
```

Wait for confirmation before applying any changes.

## Step 6 — Apply updates (on confirmation)

### Update otherness agent files
```bash
git -C ~/.otherness pull --quiet
echo "otherness: updated to $(git -C ~/.otherness rev-parse --short HEAD)"
```

### Upgrade speckit CLI
```bash
uv tool upgrade specify-cli 2>/dev/null || pip install --upgrade specify-cli 2>/dev/null
specify --version
```

### Update extensions via speckit
For each extension with an available update:
```bash
# Re-install extension (speckit handles version pinning)
specify extension update <extension-name>
```

### Update manifest checksums
After updating command files, update the manifest:
```bash
python3 - << 'EOF'
import json, hashlib, os

def sha256(path):
    with open(path, 'rb') as f:
        return hashlib.sha256(f.read()).hexdigest()

manifest = json.load(open('.specify/integrations/opencode.manifest.json'))
updated = {}
for path in manifest['files']:
    if os.path.exists(path):
        updated[path] = sha256(path)
    else:
        updated[path] = manifest['files'][path]  # keep old if file missing

manifest['files'] = updated
with open('.specify/integrations/opencode.manifest.json', 'w') as f:
    json.dump(manifest, f, indent=2)
print("Updated opencode manifest checksums.")
EOF
```

## Step 7 — Verify after update

```bash
echo ""
echo "=== Post-update verification ==="
echo "otherness: $(git -C ~/.otherness log --oneline -1 2>/dev/null)"
specify --version 2>/dev/null || echo "specify: not installed"
echo ""
echo "Run /otherness.run to start the updated agent."
```

## What each dependency provides and when to update

| Dependency | What it adds | How often to update |
|---|---|---|
| **otherness** (`~/.otherness/`) | Agent loops, GitHub PM, roles, PDCA | On every session (auto via git pull) |
| **speckit CLI** | Internal spec workflow commands | When new queue/item/verify features ship |
| **maqa extension** | State machine conventions, bounded sessions | When new coordination patterns available |
| **aide extension** | Queue/roadmap generation improvements | When roadmap-to-items translation improves |
| **verify/verify-tasks** | Phantom completion detection | When new anti-patterns are caught |
| **review extensions** | Code review sub-agents | When new review dimensions available |
