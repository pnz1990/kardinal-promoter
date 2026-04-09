---
description: Check CI pipeline status for a given branch. Returns green, red, pending,
  or unknown. Called by the MAQA coordinator before moving a feature to In Review.
  Can also be invoked directly.
---


<!-- Extension: maqa-ci -->
<!-- Config: .specify/extensions/maqa-ci/ -->
You are checking CI pipeline status for a feature branch.

## Input

$ARGUMENTS

Expected format (from coordinator):
```
branch: feature/<N>-<name>
name: <feature-name>
```

Or invoked directly with branch name as argument.

## Step 1 — Read CI config

```bash
source <(python3 -c "
import re
with open('maqa-ci/ci-config.yml') as f:
    for line in f:
        m = re.match(r'^(\w+):\s*\"?([^\"#\n]+)\"?', line.strip())
        if m and m.group(2).strip():
            print(f'{m.group(1).upper()}={m.group(2).strip()}')
")
```

## Step 2 — Check status by provider

### GitHub Actions

```bash
GH_TOKEN="${GH_TOKEN:-$(gh auth token 2>/dev/null)}"
BRANCH="<branch>"

# Get latest workflow run for this branch
STATUS=$(curl -s -H "Authorization: bearer $GH_TOKEN" \
  "https://api.github.com/repos/$GITHUB_ACTIONS_OWNER/$GITHUB_ACTIONS_REPO/actions/runs?branch=$BRANCH&per_page=5" | \
  python3 -c "
import json,sys
runs = json.load(sys.stdin).get('workflow_runs',[])
if not runs:
    print('unknown')
else:
    r = runs[0]
    conclusion = r.get('conclusion') or ''
    status = r.get('status','')
    if status == 'completed':
        print('green' if conclusion == 'success' else 'red')
    elif status in ('in_progress','queued','waiting'):
        print('pending')
    else:
        print('unknown')
")
```

### CircleCI

```bash
STATUS=$(curl -s -H "Circle-Token: $CIRCLE_TOKEN" \
  "https://circleci.com/api/v2/project/$CIRCLECI_PROJECT_SLUG/pipeline?branch=$BRANCH" | \
  python3 -c "
import json,sys
data = json.load(sys.stdin)
items = data.get('items',[])
if not items:
    print('unknown')
else:
    # Get latest pipeline's workflow status
    pipeline_id = items[0]['id']
    print(pipeline_id)  # coordinator fetches workflow from pipeline_id
")
# (full implementation polls pipeline workflows endpoint)
```

### GitLab CI

```bash
ENCODED_BRANCH=$(python3 -c "import urllib.parse; print(urllib.parse.quote('$BRANCH', safe=''))")
STATUS=$(curl -s -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
  "${GITLAB_BASE_URL:-https://gitlab.com}/api/v4/projects/$GITLAB_PROJECT_ID/pipelines?ref=$ENCODED_BRANCH&per_page=1" | \
  python3 -c "
import json,sys
items = json.load(sys.stdin)
if not items:
    print('unknown')
else:
    s = items[0].get('status','')
    mapping = {'success':'green','failed':'red','running':'pending','pending':'pending','canceled':'red'}
    print(mapping.get(s,'unknown'))
")
```

### Bitbucket Pipelines

```bash
BB_AUTH=$(echo -n "$BITBUCKET_USER:$BITBUCKET_TOKEN" | base64)
ENCODED_BRANCH=$(python3 -c "import urllib.parse; print(urllib.parse.quote('$BRANCH', safe=''))")
STATUS=$(curl -s -H "Authorization: Basic $BB_AUTH" \
  "https://api.bitbucket.org/2.0/repositories/$BITBUCKET_WORKSPACE/$BITBUCKET_REPO_SLUG/pipelines/?sort=-created_on&page=1&pagelen=1&target.ref_name=$ENCODED_BRANCH" | \
  python3 -c "
import json,sys
items = json.load(sys.stdin).get('values',[])
if not items:
    print('unknown')
else:
    state = items[0].get('state',{})
    name = state.get('name','')
    result = state.get('result',{}).get('name','')
    if name == 'COMPLETED':
        print('green' if result == 'SUCCESSFUL' else 'red')
    elif name in ('IN_PROGRESS','PENDING'):
        print('pending')
    else:
        print('unknown')
")
```

## Step 3 — Handle pending

If `STATUS=pending`, wait and poll (up to `WAIT_TIMEOUT_SECONDS`):

```bash
TIMEOUT=${WAIT_TIMEOUT_SECONDS:-300}
ELAPSED=0
while [ "$STATUS" = "pending" ] && [ "$ELAPSED" -lt "$TIMEOUT" ]; do
  sleep 15
  ELAPSED=$((ELAPSED + 15))
  # re-run the check above
done
[ "$STATUS" = "pending" ] && STATUS="timeout"
```

## Step 4 — Return result (TOON)

```
branch: <branch>
ci_status: green | red | pending | unknown | timeout
provider: <provider>
detail: <run URL or error message>
```

The coordinator reads `ci_status`:
- `green` → proceed to QA spawn
- `red` or `timeout` → add BLOCKED comment (if `block_on_red: true`), return to feature agent
- `unknown` → warn, proceed if `block_on_red: false`