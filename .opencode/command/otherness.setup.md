---
description: "One-time setup: creates otherness-config.yaml and deploys the otherness command files. Run once per project."
---

You are setting up otherness for this project.

## Step 1 — Create otherness-config.yaml if missing

```bash
if [ ! -f "otherness-config.yaml" ]; then
  AGENTS_PATH=$(ls ~/.otherness/otherness-config-template.yaml 2>/dev/null \
    && echo ~/.otherness/otherness-config-template.yaml \
    || echo "")

  if [ -n "$AGENTS_PATH" ]; then
    cp "$AGENTS_PATH" otherness-config.yaml
    echo "Created otherness-config.yaml from template."
  else
    cat > otherness-config.yaml << 'EOF'
project:
  name: my-project
  repo: owner/repo
  report_issue: 1
  board_url: ""
  pr_label: ""

maqa:
  mode: standalone
  agents_path: ~/.otherness/agents
  status_update_cycles: 5
  product_validation_cycles: 3

ci:
  provider: github-actions
  github_actions:
    workflow: ci.yml
  wait_timeout_seconds: 1200
  block_on_red: true

github_projects:
  project_id: ""
  project_number: ""
  linked_repo: owner/repo
EOF
    echo "Created otherness-config.yaml (minimal template — no ~/.otherness found)."
  fi
fi
```

## Step 2 — Auto-fill project identity from git remote

```bash
REMOTE=$(git remote get-url origin 2>/dev/null | sed 's|.*github.com[:/]||;s|\.git$||')
if [ -n "$REMOTE" ]; then
  python3 - "$REMOTE" << 'EOF'
import re, sys
repo = sys.argv[1]
name = repo.split('/')[-1]
content = open('otherness-config.yaml').read()
content = re.sub(r'(  repo:\s*)owner/repo', f'\\g<1>{repo}', content)
content = re.sub(r'(  name:\s*)my-project', f'\\g<1>{name}', content)
content = re.sub(r'(  linked_repo:\s*)owner/repo', f'\\g<1>{repo}', content)
open('otherness-config.yaml', 'w').write(content)
print(f"Set project.repo = {repo}, project.name = {name}")
EOF
fi
```

## Step 3 — Ensure ~/.otherness is cloned

```bash
if [ ! -d ~/.otherness ]; then
  REMOTE_URL=$(git remote get-url origin 2>/dev/null | sed 's|.*github.com[:/]\([^/]*\)/.*|\1|')
  git clone --quiet "git@github.com:${REMOTE_URL}/otherness.git" ~/.otherness 2>/dev/null || \
  git clone --quiet "https://github.com/${REMOTE_URL}/otherness.git" ~/.otherness 2>/dev/null || \
  echo "Could not auto-clone otherness. Clone manually: git clone git@github.com:<owner>/otherness.git ~/.otherness"
fi
```

## Step 4 — Migrate .maqa/ → .otherness/ (upgrade from older otherness versions)

```bash
if [ -d ".maqa" ] && [ ! -d ".otherness" ]; then
  mv .maqa .otherness
  echo "Migrated .maqa/ → .otherness/"
elif [ -d ".maqa" ] && [ -d ".otherness" ]; then
  echo "Both .maqa/ and .otherness/ exist — .otherness/ takes precedence. Remove .maqa/ when confirmed."
fi
```

## Step 5 — Ensure .otherness/state.json exists

```bash
mkdir -p .otherness
if [ ! -f .otherness/state.json ]; then
  REPO=$(git remote get-url origin 2>/dev/null | sed 's|.*github.com[:/]||;s|\.git$||')
  python3 - "$REPO" << 'EOF'
import json, sys, datetime
repo = sys.argv[1]
state = {
  "version": "1.3",
  "mode": "standalone",
  "repo": repo,
  "current_queue": None,
  "features": {},
  "engineer_slots": {"ENGINEER-1": None, "ENGINEER-2": None, "ENGINEER-3": None},
  "bounded_sessions": {},
  "session_heartbeats": {
    "STANDALONE": {"last_seen": None, "cycle": 0}
  },
  "handoff": None
}
with open('.otherness/state.json', 'w') as f:
    json.dump(state, f, indent=2)
print("Created .otherness/state.json")
EOF
fi
```

## Done

Edit `otherness-config.yaml` to configure your CI, GitHub Projects board, and agent settings.

Then run `/otherness.run` to start the autonomous team.
