---
description: "Bounded standalone agent. Inject your scope in the prompt — multiple sessions can run concurrently without conflicts. Each creates its own GitHub progress issue."
---

```bash
AGENTS_PATH=$(python3 -c "
import re, os
section = None
for line in open('otherness-config.yaml'):
    s = re.match(r'^(\w[\w_]*):', line)
    if s: section = s.group(1)
    if section == 'maqa':
        m = re.match(r'^\s+agents_path:\s*[\"\'']?([^\"\'#\n]+)[\"\'']?', line)
        if m: print(os.path.expanduser(m.group(1).strip())); break
" 2>/dev/null || echo "$HOME/.otherness/agents")
```

Read and follow `$AGENTS_PATH/bounded-standalone.md`.

## How to use

Start with `/otherness.run.bounded` and paste a boundary block below in your prompt.

## Pre-defined boundaries (copy and paste)

**Refactor Agent** — fix existing logic leaks:
```
AGENT_NAME=Refactor Agent
AGENT_ID=STANDALONE-REFACTOR
SCOPE=Graph purity — fix existing logic leaks in pkg/health, pkg/scm, pkg/steps, policygate reconciler. No new CRDs.
ALLOWED_AREAS=area/health,area/scm,area/policygate
ALLOWED_MILESTONES=v0.2.1
ALLOWED_PACKAGES=pkg/health,pkg/scm,pkg/steps,pkg/reconciler/policygate,pkg/reconciler/bundle,pkg/reconciler/metriccheck
DENY_PACKAGES=cmd/kardinal,web/src,api/v1alpha1,pkg/reconciler/promotionstep,pkg/graph,pkg/translator
```

**CLI Agent** — kardinal CLI commands and embedded React UI:
```
AGENT_NAME=CLI Agent
AGENT_ID=STANDALONE-CLI-UI
SCOPE=CLI and UI — kardinal commands, output formatting, policy simulate, embedded React UI
ALLOWED_AREAS=area/cli,area/ui
ALLOWED_MILESTONES=v0.2.0,v0.2.1,v0.3.0
ALLOWED_PACKAGES=cmd/kardinal,web/src,web/embed.go
DENY_PACKAGES=pkg/reconciler,pkg/graph,pkg/translator,api/v1alpha1
```

**Core Agent** — new CRDs and PromotionStep reconciler:
```
AGENT_NAME=Core Agent
AGENT_ID=STANDALONE-CORE
SCOPE=Core — new CRDs (PRStatus, RollbackPolicy, SoakTimer), PromotionStep reconciler fixes, Graph/translator
ALLOWED_AREAS=area/controller,area/graph,area/api
ALLOWED_MILESTONES=v0.2.1,v0.4.0
ALLOWED_PACKAGES=pkg/reconciler/promotionstep,pkg/reconciler/bundle,pkg/graph,pkg/translator,api/v1alpha1,config/crd,config/rbac
DENY_PACKAGES=cmd/kardinal,web/src,pkg/scm,pkg/reconciler/policygate
```

**Extensions Agent** — new SCM providers and health adapters:
```
AGENT_NAME=Extensions Agent
AGENT_ID=STANDALONE-EXTENSIONS
SCOPE=Extension points — GitLab SCM provider, ArgoRollouts health adapter, update strategies
ALLOWED_AREAS=area/scm,area/health
ALLOWED_MILESTONES=v0.4.0
ALLOWED_PACKAGES=pkg/scm,pkg/health,pkg/update,pkg/steps
DENY_PACKAGES=pkg/reconciler/promotionstep,pkg/reconciler/policygate,pkg/graph,api/v1alpha1,cmd/kardinal
```
