{{/*
Copyright 2026 The kardinal-promoter Authors.
Licensed under the Apache License, Version 2.0
*/}}

{{/*
RBAC rules for the controller ServiceAccount, grouped by where the controller
makes the call. Each helper is a YAML list at column 0; include it under
`rules:` with `nindent 2`.

  rules.namespaced  every namespace the controller reconciles. Cluster mode:
                    ClusterRole <fullname>-manager-role. Namespace mode: Role
                    <fullname>-manager-role in controller.watchNamespace.
  rules.cluster     cluster-scoped kinds. Cluster mode: the same ClusterRole.
                    Namespace mode: ClusterRole <fullname>-cluster-scoped.
  rules.release     the controller's own namespace (leader election, the
                    kardinal-version ConfigMap and the SCM token Secret). Role
                    <fullname>-leader-election.

test/helm/chart_render_test.go (controllerAccess) lists each API call these
rules exist for. A new client call needs a row there and a rule here.
*/}}

{{- define "kardinal-promoter.rules.namespaced" -}}
# Events from every reconciler (events.k8s.io/v1 recorder).
- apiGroups: ["events.k8s.io"]
  resources: ["events"]
  verbs: ["create", "patch"]
# core/v1 Events: the UI step event list reads them (the API server serves the
# events.k8s.io Events there too), and leader election writes them.
- apiGroups: [""]
  resources: ["events"]
  verbs: ["get", "list", "watch", "create", "patch"]
# Pipeline git credentials (spec.git.secretRef) and the SCM token Secret.
# get only: Secret reads bypass the cache (manager_options.go uncachedObjects)
# and every reader uses Get, so the controller cannot list or watch Secrets.
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get"]
# No ConfigMap rule: the only ConfigMap the controller touches is
# kardinal-version, read uncached by name (manager_options.go uncachedObjects)
# and written through rules.release.
# kardinal.io kinds and their status subresources.
- apiGroups: ["kardinal.io"]
  resources:
    - pipelines
    - bundles
    - policygates
    - rollbackpolicies
    - subscriptions
    - promotionsteps
    - prstatuses
    - metricchecks
    - scheduleclocks
    - notificationhooks
    - hookruns
    - renderruns
    - imageverifications
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
# ScmProviders: read (the translator, reconcilers and the webhook endpoint)
# and their Ready condition written; the controller never creates them.
- apiGroups: ["kardinal.io"]
  resources: ["scmproviders"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["kardinal.io"]
  resources: ["scmproviders/status"]
  verbs: ["get", "update", "patch"]
- apiGroups: ["kardinal.io"]
  resources:
    - pipelines/status
    - bundles/status
    - policygates/status
    - rollbackpolicies/status
    - subscriptions/status
    - promotionsteps/status
    - prstatuses/status
    - metricchecks/status
    - scheduleclocks/status
    - notificationhooks/status
    - hookruns/status
    - renderruns/status
    - imageverifications/status
  verbs: ["get", "update", "patch"]
# Pipeline hooks (docs/hooks.md) and rendered manifests
# (docs/rendered-manifests.md): the HookRun and RenderRun reconcilers create
# each hook's and render's Job, owned by its run, and delete one that ran
# past its timeout. The informer caches only Jobs labelled kardinal.io/run-job.
- apiGroups: ["batch"]
  resources: ["jobs"]
  verbs: ["get", "list", "watch", "create", "delete"]
# Audit records are append-only. audit.retention.enabled adds delete: the
# leader deletes records past their retention (pkg/reconciler/auditretention).
- apiGroups: ["kardinal.io"]
  resources: ["auditevents"]
  {{- if .Values.audit.retention.enabled }}
  verbs: ["get", "list", "watch", "create", "delete"]
  {{- else }}
  verbs: ["get", "list", "watch", "create"]
  {{- end }}
# kro Graphs (one per Bundle, kro.run/v1alpha1).
- apiGroups: ["kro.run"]
  resources: ["graphs"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
- apiGroups: ["kro.run"]
  resources: ["graphs/status"]
  verbs: ["get"]
# Graph identity: the ServiceAccount kro impersonates and its RoleBindings.
# bind is limited to the two Graph ClusterRoles, so the controller cannot
# grant anything else. delete prunes reader RoleBindings no Graph reads
# through any more (C01-graph-04): after a translation, when a Graph is
# deleted, and in the leader's sweep, which lists the RoleBindings carrying
# the controller's managed-by label (cluster mode only).
# The render Jobs' ServiceAccount (kardinal-render, no role, no token) is
# created the same way in a namespace that has none.
- apiGroups: [""]
  resources: ["serviceaccounts"]
  verbs: ["get", "create"]
- apiGroups: ["rbac.authorization.k8s.io"]
  resources: ["rolebindings"]
  verbs: ["get", "list", "create", "update", "delete"]
- apiGroups: ["rbac.authorization.k8s.io"]
  resources: ["clusterroles"]
  verbs: ["bind"]
  resourceNames:
    - {{ include "kardinal-promoter.graphApplierRole" . }}
    - {{ include "kardinal-promoter.graphReaderRole" . }}
# Health adapters (pkg/health): resource, argocd, argoRollouts, flux, flagger.
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: ["get", "list", "watch"]
# The resource and flux adapters read the ReplicaSet a Deployment's
# ProgressDeadlineExceeded names, by name and uncached.
- apiGroups: ["apps"]
  resources: ["replicasets"]
  verbs: ["get"]
# The resource adapter lists the pods of a Deployment's new ReplicaSet,
# uncached and only while replicas are unavailable, to name why a new pod is
# not ready (ErrImagePull, CrashLoopBackOff). In cluster mode this covers
# every pod in the cluster (docs/guides/security.md#controller-rbac).
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["list"]
- apiGroups: ["argoproj.io"]
  resources: ["applications"]
  {{- if .Values.rbac.argocdApplicationsWrite }}
  # rbac.argocdApplicationsWrite: the argocd update strategy patches the
  # Application's image overrides.
  verbs: ["get", "list", "watch", "patch"]
  {{- else }}
  verbs: ["get", "list", "watch"]
  {{- end }}
- apiGroups: ["argoproj.io"]
  resources: ["rollouts"]
  verbs: ["get", "list", "watch"]
# Argo Rollouts analysis (spec.verification): the translator reads the
# AnalysisTemplates a Pipeline names, uncached, by name (pkg/translator/analysis.go).
- apiGroups: ["argoproj.io"]
  resources: ["analysistemplates"]
  verbs: ["get"]
- apiGroups: ["kustomize.toolkit.fluxcd.io"]
  resources: ["kustomizations"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["flagger.app"]
  resources: ["canaries"]
  verbs: ["get", "list", "watch"]
{{- end }}

{{- define "kardinal-promoter.rules.cluster" -}}
# ClusterAnalysisTemplates a Pipeline's spec.verification names, read by the
# translator uncached, by name (pkg/translator/analysis.go).
- apiGroups: ["argoproj.io"]
  resources: ["clusteranalysistemplates"]
  verbs: ["get"]
# ChangeWindow is cluster-scoped: PolicyGates read it, and its reconciler
# writes status.
- apiGroups: ["kardinal.io"]
  resources: ["changewindows"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["kardinal.io"]
  resources: ["changewindows/status"]
  verbs: ["get", "update", "patch"]
# ClusterScmProviders, read like ScmProviders and their Ready condition
# written.
- apiGroups: ["kardinal.io"]
  resources: ["clusterscmproviders"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["kardinal.io"]
  resources: ["clusterscmproviders/status"]
  verbs: ["get", "update", "patch"]
# The Graph cleanup reconciler reads the namespace of a deleted Graph, the
# Bundle reconciler the namespace of a Bundle before translating it, and the
# PromotionStep reconciler the namespace of a deleted step that holds a PR, to
# tell whether it is being deleted (pkg/reconciler/graphcleanup,
# pkg/reconciler/bundle, pkg/reconciler/promotionstep). Namespace mode: only
# the watched namespace.
- apiGroups: [""]
  resources: ["namespaces"]
  verbs: ["get"]
  {{- with .Values.controller.watchNamespace }}
  resourceNames: [{{ . | quote }}]
  {{- end }}
{{- if .Values.controller.namespaceShard }}
# controller.namespaceShard: the shard gate (pkg/shard) watches Namespaces for
# their kardinal.io/shard label and holds the token Lease kardinal-shard in
# each namespace it reconciles. The cache lists and watches only that name
# (field selector metadata.name), so resourceNames limits list and watch too.
- apiGroups: [""]
  resources: ["namespaces"]
  verbs: ["list", "watch"]
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  resourceNames: ["kardinal-shard"]
  verbs: ["get", "list", "watch", "update"]
# The other shards' heartbeats (kardinal-shard-heartbeat-<shard>, in each
# shard's namespace), read only: whether a token's holder is alive, and
# (default shard) which shards run. Their names include shard names the chart
# does not know, so get and list cannot be limited by name; there is no watch
# and no write. This shard's own heartbeat is written through rules.release.
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["get", "list"]
# A token is created the first time a namespace is taken; create cannot be
# limited by resourceNames.
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["create"]
{{- end }}

# The PolicyGate reconciler records an override's createdBy as verified only
# while the chart's gate-overrides admission policy and its binding exist
# (--override-identity-policy): get on those two objects by name.
- apiGroups: ["admissionregistration.k8s.io"]
  resources: ["validatingadmissionpolicies", "validatingadmissionpolicybindings"]
  verbs: ["get"]
  resourceNames: [{{ printf "%s-gate-overrides" (include "kardinal-promoter.fullname" .) | quote }}]
{{- if .Values.ui.auth.tokenReview }}
# ui.auth.tokenReview: the UI API validates each bearer token with a
# TokenReview and authorizes it with a SubjectAccessReview.
- apiGroups: ["authentication.k8s.io"]
  resources: ["tokenreviews"]
  verbs: ["create"]
- apiGroups: ["authorization.k8s.io"]
  resources: ["subjectaccessreviews"]
  verbs: ["create"]
{{- end }}
{{- end }}

{{- define "kardinal-promoter.rules.release" -}}
# Leader election Lease (controller-runtime creates it in the Pod namespace).
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
# The kardinal-version ConfigMap written at startup. create cannot be limited
# by resourceNames.
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["create"]
- apiGroups: [""]
  resources: ["configmaps"]
  resourceNames: ["kardinal-version"]
  verbs: ["get", "update", "patch"]
{{- with include "kardinal-promoter.githubSecretName" . }}
# The SCM token Secret, polled by the SecretWatcher to reload a rotated token.
# Rendered only when a name is set: empty resourceNames would mean every Secret.
- apiGroups: [""]
  resources: ["secrets"]
  resourceNames: [{{ . | quote }}]
  verbs: ["get"]
{{- end }}
{{- end }}
