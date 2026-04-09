---
description: Detect CI provider from repo structure and generate maqa-ci/ci-config.yml.
  Supports GitHub Actions, CircleCI, GitLab CI, and Bitbucket Pipelines. Run once
  per project.
---


<!-- Extension: maqa-ci -->
<!-- Config: .specify/extensions/maqa-ci/ -->
You are setting up CI/CD integration for MAQA.

## Step 1 — Auto-detect provider

```bash
PROVIDER="none"
[ -d ".github/workflows" ] && ls .github/workflows/*.yml 2>/dev/null | head -1 && PROVIDER="github-actions"
[ -f ".circleci/config.yml" ] && PROVIDER="circleci"
[ -f ".gitlab-ci.yml" ] && PROVIDER="gitlab"
[ -f "bitbucket-pipelines.yml" ] && PROVIDER="bitbucket"

echo "Detected: $PROVIDER"
```

Report detected provider to user. Ask them to confirm or select a different one.

## Step 2 — Collect provider-specific config

### GitHub Actions

```bash
OWNER=$(gh repo view --json owner -q .owner.login 2>/dev/null || git remote get-url origin | python3 -c "import sys,re; m=re.search(r'github\.com[:/]([^/]+)/([^/.]+)', sys.stdin.read()); print(m.group(1) if m else '')")
REPO=$(gh repo view --json name -q .name 2>/dev/null || git remote get-url origin | python3 -c "import sys,re; m=re.search(r'github\.com[:/]([^/]+)/([^/.]+)', sys.stdin.read()); print(m.group(2) if m else '')")

# List workflows
GH_TOKEN="${GH_TOKEN:-$(gh auth token 2>/dev/null)}"
curl -s -H "Authorization: bearer $GH_TOKEN" \
  "https://api.github.com/repos/$OWNER/$REPO/actions/workflows" | \
  python3 -c "
import json,sys
for w in json.load(sys.stdin).get('workflows',[]):
    print(f\"{w['path'].split('/')[-1]:30} — {w['name']}\")
"
```

Ask which workflow to monitor (or leave empty to check all).

### CircleCI

```bash
# Derive project slug from git remote
REMOTE=$(git remote get-url origin)
python3 -c "
import re
m = re.search(r'github\.com[:/]([^/]+)/([^/.]+)', '$REMOTE')
if m: print(f'github/{m.group(1)}/{m.group(2)}')
"
```

### GitLab CI

```bash
# Get project ID from GitLab API
REMOTE=$(git remote get-url origin)
ENCODED=$(python3 -c "import urllib.parse; print(urllib.parse.quote('$REMOTE'.split('gitlab.com/')[-1].replace('.git',''), safe=''))")
curl -s -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
  "https://gitlab.com/api/v4/projects/$ENCODED" | \
  python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('id',''))"
```

### Bitbucket

Ask user for workspace and repo slug.

## Step 3 — Write config

```bash
mkdir -p maqa-ci
cat > maqa-ci/ci-config.yml << EOF
# MAQA CI/CD Configuration — generated $(date -Iseconds)
provider: "$PROVIDER"

github_actions:
  owner: "$OWNER"
  repo: "$REPO"
  workflow: "$WORKFLOW"

circleci:
  org_slug: "$CIRCLE_ORG"
  project_slug: "$CIRCLE_PROJECT"

gitlab:
  base_url: "https://gitlab.com"
  project_id: "$GITLAB_PROJECT_ID"

bitbucket:
  workspace: "$BB_WORKSPACE"
  repo_slug: "$BB_REPO"

wait_timeout_seconds: 300
block_on_red: true
EOF
```

## Done

Report provider and tell user the coordinator will now gate In Review on green CI.