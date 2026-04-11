---
description: "Bounded standalone agent. Inject your boundary fields directly in the prompt after this command — no files needed. Multiple sessions can run concurrently."
---
description: "Bounded standalone agent. Inject boundary in prompt. Creates a dedicated GitHub issue for hourly progress reports. Multiple sessions run concurrently without conflicts."
---

Read and follow `$AGENTS_PATH/bounded-standalone.md` (AGENTS_PATH from maqa-config.yml).

```bash
AGENTS_PATH=$(python3 -c "
import re, os
for line in open('maqa-config.yml'):
    m = re.match(r'^agents_path:\s*[\"\'']?([^\"\'#\n]+)[\"\'']?', line.strip())
    if m: print(os.path.expanduser(m.group(1).strip())); break
" 2>/dev/null)
```

## How to use

Start with `/speckit.maqa.bounded-standalone` and paste a boundary block below in your prompt.
Each session creates its own `[AGENT_NAME] Progress Log` GitHub issue with hourly updates.

## Boundary blocks (copy-paste directly into prompt)

**Refactor Agent** — fix existing logic leaks in health/scm/steps/policygate:
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

**Refactor (existing code cleanup — NO new CRDs, v0.2.1):**
```
AGENT_ID=STANDALONE-REFACTOR
SCOPE=Graph purity refactor — fix existing logic leaks in health/scm/steps/policygate/bundle (no new CRDs, no PromotionStep)
ALLOWED_AREAS=area/health,area/scm,area/policygate
ALLOWED_MILESTONES=v0.2.1
ALLOWED_PACKAGES=pkg/health,pkg/scm,pkg/steps,pkg/reconciler/policygate,pkg/reconciler/bundle,pkg/reconciler/metriccheck
DENY_PACKAGES=cmd/kardinal,web/src,api/v1alpha1,pkg/reconciler/promotionstep,pkg/graph,pkg/translator
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

**Core features (new CRDs + PromotionStep reconciler, v0.2.1 and v0.4.0):**
```
AGENT_ID=STANDALONE-CORE
SCOPE=Core features — new CRDs (PRStatus, RollbackPolicy, SoakTimer), PromotionStep reconciler fixes, Graph/translator
ALLOWED_AREAS=area/controller,area/graph,area/api
ALLOWED_MILESTONES=v0.2.1,v0.4.0
ALLOWED_PACKAGES=pkg/reconciler/promotionstep,pkg/reconciler/bundle,pkg/graph,pkg/translator,api/v1alpha1,config/crd,config/rbac
DENY_PACKAGES=cmd/kardinal,web/src,pkg/scm,pkg/reconciler/policygate
```

**Extension points (SCM, health adapters, v0.4.0):**
```
AGENT_ID=STANDALONE-EXTENSIONS
SCOPE=Extension points — new SCM providers (GitLab, Forgejo), health adapters (argoRollouts), update strategies
ALLOWED_AREAS=area/scm,area/health
ALLOWED_MILESTONES=v0.4.0
ALLOWED_PACKAGES=pkg/scm,pkg/health,pkg/update,pkg/steps
DENY_PACKAGES=pkg/reconciler/promotionstep,pkg/reconciler/policygate,pkg/graph,api/v1alpha1,cmd/kardinal
```
