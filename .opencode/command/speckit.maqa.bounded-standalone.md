---
description: "Bounded standalone agent. Inject your boundary fields directly in the prompt after this command — no files needed. Multiple sessions can run concurrently."
---

Read and follow `$AGENTS_PATH/bounded-standalone.md` (where AGENTS_PATH comes from maqa-config.yml).

```bash
AGENTS_PATH=$(python3 -c "
import re, os
for line in open('maqa-config.yml'):
    m = re.match(r'^agents_path:\s*[\"\'']?([^\"\'#\n]+)[\"\'']?', line.strip())
    if m: print(os.path.expanduser(m.group(1).strip())); break
" 2>/dev/null)
```

## How to inject your boundary

When starting this session, add the boundary fields after the command in your prompt:

```
AGENT_ID=STANDALONE-REFACTOR
SCOPE=Graph purity refactor — eliminate logic leaks (v0.2.1, krocodile-independent only)
ALLOWED_AREAS=area/controller,area/health,area/scm,area/graph,area/policygate
ALLOWED_MILESTONES=v0.2.1
ALLOWED_PACKAGES=pkg/reconciler,pkg/health,pkg/scm,pkg/steps,pkg/graph,pkg/translator,api/v1alpha1
DENY_PACKAGES=cmd/kardinal,web/src
```

The agent will parse these fields from the conversation context. No BOUNDARY file needed.

## Pre-defined boundaries (copy and paste)

**Refactor (graph purity, v0.2.1):**
```
AGENT_ID=STANDALONE-REFACTOR
SCOPE=Graph purity refactor — eliminate logic leaks from docs/design/11-graph-purity-tech-debt.md (v0.2.1, krocodile-independent only)
ALLOWED_AREAS=area/controller,area/health,area/scm,area/graph,area/policygate
ALLOWED_MILESTONES=v0.2.1
ALLOWED_PACKAGES=pkg/reconciler,pkg/health,pkg/scm,pkg/steps,pkg/graph,pkg/translator,api/v1alpha1
DENY_PACKAGES=cmd/kardinal,web/src
```

**CLI and UI:**
```
AGENT_ID=STANDALONE-CLI-UI
SCOPE=CLI and UI — kardinal commands, output formatting, embedded React UI
ALLOWED_AREAS=area/cli,area/ui
ALLOWED_MILESTONES=v0.2.0,v0.2.1,v0.3.0
ALLOWED_PACKAGES=cmd/kardinal,web/src,web/embed.go
DENY_PACKAGES=pkg/reconciler,pkg/graph,pkg/translator,api/v1alpha1
```

**Core features (new CRDs, Graph integration):**
```
AGENT_ID=STANDALONE-CORE
SCOPE=Core features — new CRDs (PRStatus, RollbackPolicy), Graph integration, reconcilers
ALLOWED_AREAS=area/controller,area/graph,area/policygate,area/api
ALLOWED_MILESTONES=v0.2.1,v0.4.0
ALLOWED_PACKAGES=pkg/reconciler,pkg/graph,pkg/translator,api/v1alpha1,config/crd,config/rbac
DENY_PACKAGES=cmd/kardinal,web/src,pkg/scm
```

**Extension points (SCM, health adapters):**
```
AGENT_ID=STANDALONE-EXTENSIONS
SCOPE=Extension points — new SCM providers (GitLab, Forgejo), health adapters (argoRollouts), update strategies
ALLOWED_AREAS=area/scm,area/health
ALLOWED_MILESTONES=v0.4.0
ALLOWED_PACKAGES=pkg/scm,pkg/health,pkg/update,pkg/steps
DENY_PACKAGES=pkg/reconciler/promotionstep,pkg/reconciler/policygate,pkg/graph,api/v1alpha1,cmd/kardinal
```
