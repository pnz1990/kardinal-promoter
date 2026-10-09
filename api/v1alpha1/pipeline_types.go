// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PipelineSpec defines the desired state of a Pipeline.
type PipelineSpec struct {
	// Git holds the shared GitOps repository configuration for all
	// environments in this pipeline.
	Git PipelineGit `json:"git"`

	// Environments lists the promotion path.
	// Sequential ordering (GB-1): when an environment does not specify dependsOn,
	// it implicitly depends on the previous entry in this list. The first environment
	// has no upstream dependency. This sequential default means a list of N environments
	// without dependsOn fields produces a linear chain. Override with dependsOn to
	// express parallel fan-out or explicit DAG structure.
	// Environment names must be unique; at most 100 environments (a bound
	// the API server needs to cost the CEL rules on each entry).
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=100
	// +listType=map
	// +listMapKey=name
	Environments []EnvironmentSpec `json:"environments"`

	// PolicyGates is not implemented, and the API server rejects a non-empty
	// list. Org gates apply through the kardinal.io/applies-to label.
	//
	// Deprecated: remove the field; label org PolicyGates with
	// kardinal.io/applies-to instead.
	// +kubebuilder:validation:XValidation:rule="size(self) == 0",message="spec.policyGates is not implemented; remove it (org gates use the kardinal.io/applies-to label)"
	// +optional
	PolicyGates []PipelinePolicyGateRef `json:"policyGates,omitempty"`

	// Paused suspends all promotions in this pipeline when true.
	// +kubebuilder:default=false
	// +optional
	Paused bool `json:"paused,omitempty"`

	// HistoryLimit is the number of completed Bundle promotions to retain.
	// When unset or zero, defaults to 50. Terminal Bundles (Verified, Failed, Superseded)
	// beyond this limit are deleted oldest-first on each new Bundle creation.
	// +kubebuilder:default=50
	// +kubebuilder:validation:Minimum=1
	// +optional
	HistoryLimit int `json:"historyLimit,omitempty"`

	// PolicyNamespaces lists extra namespaces to read PolicyGates from. It only
	// adds to the list: the controller's org policy namespaces (--policy-namespaces,
	// default "platform-policies") and the pipeline's own namespace are always read,
	// so a Pipeline cannot opt out of org gates. Gates found only through this
	// field are team gates: they never count as org gates and never grant a skip.
	// +optional
	PolicyNamespaces []string `json:"policyNamespaces,omitempty"`

	// MaxConcurrentPromotions caps the number of Bundles in Promoting phase for this
	// pipeline at any given time. When 0 or unset (default), there is no cap and all
	// Available Bundles are promoted concurrently. When set to a positive value, Bundles
	// that exceed the cap are requeued until a promotion slot becomes available.
	// This prevents promotion storms (e.g. a CI burst creating 50 Bundles simultaneously)
	// from saturating git hosts, exhausting GitHub API rate limits, or creating merge
	// conflicts in the GitOps repository.
	//
	// Example: maxConcurrentPromotions: 2 allows at most 2 active promotions at once.
	// Additional Available Bundles wait in a 30-second polling loop.
	//
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxConcurrentPromotions int `json:"maxConcurrentPromotions,omitempty"`
}

// PipelineGit holds the shared GitOps repository configuration for a Pipeline.
type PipelineGit struct {
	// URL is the GitOps repository URL (HTTPS).
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// Branch is the base branch: git-clone checks it out, approval: auto
	// pushes to it, and pr-review PRs target it. Defaults to main.
	// +kubebuilder:default=main
	// +optional
	Branch string `json:"branch,omitempty"`

	// Layout controls how environment paths are organized in the repository.
	// "directory": environments as subdirectories on one branch (default).
	// "branch": environments as separate branches.
	// +kubebuilder:validation:Enum=directory;branch
	// +kubebuilder:default=directory
	// +optional
	Layout string `json:"layout,omitempty"`

	// Provider is ignored. One controller serves one SCM, chosen with its
	// --scm-provider flag.
	//
	// Deprecated: ignored; the controller's --scm-provider flag selects the provider.
	// +kubebuilder:validation:Enum=github;gitlab
	// +optional
	Provider string `json:"provider,omitempty"`

	// SecretRef references a Kubernetes Secret containing the SCM token.
	// +optional
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// SecretRef is a reference to a Kubernetes Secret by name and optional namespace.
type SecretRef struct {
	// Name is the Secret name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace is the Secret namespace. If empty, the Pipeline's namespace is used.
	// For spec.git.secretRef it must be empty or equal to the Pipeline's namespace:
	// the controller refuses to read a Secret from another namespace and fails the
	// PromotionStep with a clear message, so a Pipeline author cannot borrow another
	// team's credentials.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// EnvironmentSpec defines one environment in a Pipeline.
// +kubebuilder:validation:XValidation:rule="!(has(self.update) && has(self.update.strategy) && self.update.strategy == 'argocd' && has(self.approval) && self.approval == 'pr-review')",message="environments[]: update.strategy argocd patches the Application directly and cannot honour approval: pr-review; use approval: auto with a PolicyGate, or a git-based strategy (kustomize or helm) for a reviewed promotion"
// +kubebuilder:validation:XValidation:rule="!has(self.autoRollback)",message="environments[].autoRollback is not implemented; remove it (automatic rollback is configured with onHealthFailure, see docs/rollback.md)"
// +kubebuilder:validation:XValidation:rule="!has(self.steps) || size(self.steps) == 0",message="environments[].steps is not supported: kardinal has no custom step engine and every environment runs the default step sequence; remove it (see docs/pipeline-reference.md#promotion-steps)"
// +kubebuilder:validation:XValidation:rule="!has(self.promotionTemplate)",message="environments[].promotionTemplate is not supported: the PromotionTemplate CRD was removed and every environment runs the default step sequence; remove it (see docs/pipeline-reference.md#promotion-steps)"
// +kubebuilder:validation:XValidation:rule="!(self.name in ['api-version','kind','metadata','namespace','spec','status','graph','graphengine','kro','each','item','items','object','self','this','context','true','false','null','in','as','break','const','continue','else','for','function','if','import','let','loop','package','return','var','void','while','bundle','time'])",message="reserved environment name: the name becomes a kro Graph node ID; bundle, time, kro reserved IDs (spec, status, metadata, graph, self, each, item, ...) and CEL keywords are not allowed; rename the environment"
// +kubebuilder:validation:XValidation:rule="!has(self.pr) || (has(self.approval) && self.approval == 'pr-review')",message="environments[].pr configures the promotion pull request and needs approval: pr-review"
type EnvironmentSpec struct {
	// Name is the environment identifier (e.g. "test", "uat", "prod").
	// It must be a DNS label (lower-case letters, digits and '-', at most 63
	// characters): it names a Graph node, derived objects and, for the
	// resource, argoRollouts and flagger health checks, a namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Path is the subdirectory within the GitOps repository for this environment.
	// Used when spec.git.layout is "directory". Defaults to "environments/<name>".
	// +optional
	Path string `json:"path,omitempty"`

	// Approval controls whether promotion into this environment requires a PR review.
	// +kubebuilder:validation:Enum=auto;pr-review
	// +kubebuilder:default=auto
	// +optional
	Approval string `json:"approval,omitempty"`

	// Update holds the manifest update configuration for this environment.
	// +optional
	Update UpdateConfig `json:"update,omitempty"`

	// Health holds the health check configuration for this environment.
	// +optional
	Health HealthConfig `json:"health,omitempty"`

	// Delivery holds in-cluster progressive delivery delegation configuration.
	// +optional
	Delivery DeliveryConfig `json:"delivery,omitempty"`

	// DependsOn lists names of other environments in this pipeline that must
	// reach Verified state before this environment can start.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// Wave assigns this environment to a numbered deployment wave (K-06).
	// Environments with the same wave number are promoted in parallel. An
	// environment of a wave depends on every environment of the next lower wave
	// present; gaps in the numbering (10, 20, 30) are allowed and create no roots.
	// Without DependsOn, a wave also follows the last environment without a wave
	// listed before its first environment, so a wave after "staging" starts once
	// staging is verified, and an environment without a wave follows the
	// environment listed before it, or every environment of that one's wave.
	// DependsOn replaces these list-order edges but never the edges to the
	// previous wave. Only the first listed environment is a root unless
	// DependsOn says otherwise.
	// +kubebuilder:validation:Minimum=1
	// +optional
	Wave int `json:"wave,omitempty"`

	// Shard was the agent shard of distributed mode, which was removed. A
	// non-empty value sets the Pipeline Ready=False (reason NotImplemented)
	// and fails the environment's PromotionSteps with "shard is not
	// supported".
	//
	// Deprecated: remove shard; the controller reconciles every environment.
	// For workloads in other clusters, use the Argo CD or Flux hub (see
	// docs/distributed-mode.md).
	// +optional
	Shard string `json:"shard,omitempty"`

	// AutoRollback is reserved and rejected by the API server: consecutive-failure
	// auto-rollback is not implemented. See OnHealthFailure.
	// +optional
	AutoRollback *AutoRollbackSpec `json:"autoRollback,omitempty"`

	// Bake configures a contiguous-healthy soak window for this environment (K-01).
	// When set, the health check must pass continuously for Bake.Minutes before
	// the step transitions to Verified. A health failure resets the timer if
	// policy is "reset-on-alarm" (default), or fails the step if "fail-on-alarm".
	// +optional
	Bake *BakeConfig `json:"bake,omitempty"`

	// OnHealthFailure controls what the reconciler does when health fails during
	// bake or health checking (K-03).
	// "rollback": create a rollback Bundle at the previous version; step → RollingBack.
	// "abort": freeze the step; state → AbortedByAlarm; requires human intervention.
	// "none" (default): step → Failed; downstream stops.
	// +kubebuilder:validation:Enum=rollback;abort;none
	// +kubebuilder:default=none
	// +optional
	OnHealthFailure string `json:"onHealthFailure,omitempty"`

	// Layout configures how the promotion interacts with the Git repo layout.
	// "directory" (default): env manifests are in a subdirectory of the main branch.
	// "branch": rendered manifests are committed to a separate env-specific branch.
	//   In this mode the step sequence includes kustomize-build to render templates
	//   before committing to the target branch.
	// +kubebuilder:validation:Enum=directory;branch
	// +kubebuilder:default=directory
	// +optional
	Layout string `json:"layout,omitempty"`

	// Steps is not supported. kardinal has no custom step engine: every
	// environment runs the default step sequence (see DefaultSequenceForBundle).
	// The API server rejects a Pipeline that sets it, and so do Graph
	// translation and "kardinal validate".
	//
	// Deprecated: remove it; the step sequence follows the Bundle type,
	// update.strategy, approval and layout. See docs/pipeline-reference.md#promotion-steps.
	//
	// +optional
	Steps []StepSpec `json:"steps,omitempty"`

	// PromotionTemplate is not supported, and the PromotionTemplate CRD was
	// removed. The API server rejects a Pipeline that sets it, and so do Graph
	// translation and "kardinal validate".
	//
	// Deprecated: remove it; every environment runs the default step sequence.
	// See docs/pipeline-reference.md#promotion-steps.
	//
	// +optional
	PromotionTemplate *PromotionTemplateRef `json:"promotionTemplate,omitempty"`

	// WaitForMergeTimeout is the maximum duration a PromotionStep will wait
	// in the WaitingForMerge state before transitioning to Failed. When not set
	// or zero, the step waits indefinitely (no timeout). Accepts Go duration
	// strings: "24h", "72h", "168h", etc.
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	WaitForMergeTimeout string `json:"waitForMergeTimeout,omitempty"`

	// PR configures the pull request a pr-review environment opens: its
	// title and body, labels, reviewers, assignees, and whether the SCM merges
	// it on its own once its checks and approvals pass (auto-merge). Every
	// string is a Go text/template over the Bundle and the environment (see
	// docs/pr-evidence.md#customising-the-pr). The Pipeline is
	// Ready=False/ValidationFailed when a template does not parse or
	// execute, and a control the SCM provider does not support fails the
	// step before the PR is opened (docs/scm-providers.md#pr-controls).
	// +optional
	PR *PRConfig `json:"pr,omitempty"`

	// StepTimeoutSeconds is the maximum number of seconds a single promotion
	// step (git-clone, kustomize-set-image, open-pr, etc.) may run. The
	// reconciler cancels a step that runs longer via context.WithTimeout and
	// handles the timeout like any other step error: the step is retried with
	// backoff (10s, 20s, 40s, 80s, then 2m), and the PromotionStep is marked
	// Failed when the 5 retries are used up. When not set or 0 (default), no
	// per-step timeout is applied.
	// Useful for restricting execution in restricted-egress environments where
	// git-clone against a slow SCM host can block the reconciler indefinitely.
	// +kubebuilder:validation:Minimum=1
	// +optional
	StepTimeoutSeconds int `json:"stepTimeoutSeconds,omitempty"`

	// Regions is not supported: every region would edit the same path and push
	// the same branch. With two or more regions the Pipeline is Ready=False
	// (reason NotImplemented) and every Bundle fails when its Graph is built
	// with "regions is not supported". One region has no effect.
	//
	// Deprecated: declare one environment per region (prod-us, prod-eu) and
	// use wave.
	// +optional
	Regions []string `json:"regions,omitempty"`
}

// PRConfig configures the pull request of a pr-review environment.
type PRConfig struct {
	// TitleTemplate replaces the default title "[kardinal] Promote <bundle>
	// to <env>" (or the rollback title). Newlines become spaces; an empty
	// result fails the step.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	TitleTemplate string `json:"titleTemplate,omitempty"`

	// BodyTemplate replaces the default body. The evidence sections are
	// template functions (evidence, provenanceTable, gatesTable,
	// upstreamTable, rollbackNotice), so a custom body can keep them.
	// +kubebuilder:validation:MaxLength=16384
	// +optional
	BodyTemplate string `json:"bodyTemplate,omitempty"`

	// Labels are added to the kardinal labels (kardinal,
	// kardinal/promotion, kardinal/rollback). Each entry is a template; each
	// line of its output is one label, and empty lines are dropped.
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MaxLength=256
	// +optional
	Labels []string `json:"labels,omitempty"`

	// Reviewers are the users asked to review the PR (templates, one user
	// per output line). Usernames on GitHub, GitLab, Forgejo and Gitea; account
	// IDs or {UUID}s on Bitbucket Cloud; identity IDs on Azure DevOps.
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MaxLength=256
	// +optional
	Reviewers []string `json:"reviewers,omitempty"`

	// TeamReviewers are the teams asked to review the PR (templates, one team
	// slug per output line): GitHub and Forgejo/Gitea organisation teams, or
	// group identity IDs on Azure DevOps.
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MaxLength=256
	// +optional
	TeamReviewers []string `json:"teamReviewers,omitempty"`

	// Assignees are the users the PR is assigned to (templates, one user per
	// output line). "{{ .Bundle.Author }}" assigns the author recorded in the
	// Bundle's provenance.
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MaxLength=256
	// +optional
	Assignees []string `json:"assignees,omitempty"`

	// Merge asks the SCM to merge the PR on its own once the repository's
	// required checks and approvals pass.
	// +optional
	Merge *PRMergeConfig `json:"merge,omitempty"`
}

// PRMergeConfig configures auto-merge of a promotion PR.
// +kubebuilder:validation:XValidation:rule="self.auto || (!has(self.method) && !has(self.commitMessageTemplate) && !has(self.allowImmediate))",message="pr.merge.method, pr.merge.commitMessageTemplate and pr.merge.allowImmediate apply only to the merge kardinal asks the SCM for; set pr.merge.auto: true"
type PRMergeConfig struct {
	// Auto enables the SCM's auto-merge on the PR once kardinal has opened
	// it (GitHub auto-merge, GitLab auto-merge, Forgejo/Gitea scheduled
	// merge, Azure DevOps auto-complete, Bitbucket Data Center auto-merge):
	// the SCM merges it once the required checks and reviews pass. kardinal
	// turns auto-merge off while the Pipeline is paused or a required gate is
	// closed, and on again after. A PR with nothing pending is left for a
	// merge by hand unless allowImmediate is set.
	// +optional
	Auto bool `json:"auto,omitempty"`

	// AllowImmediate lets kardinal merge the PR at once when nothing is
	// pending on it (no required check, review or pipeline). This skips
	// human review and any CI the repository does not require.
	// +optional
	AllowImmediate bool `json:"allowImmediate,omitempty"`

	// Method is how the SCM merges the PR: merge (a merge commit), squash
	// or rebase. Empty uses merge.
	// +kubebuilder:validation:Enum=merge;squash;rebase
	// +optional
	Method string `json:"method,omitempty"`

	// CommitMessageTemplate is the message of the merge (or squash) commit, a
	// template like titleTemplate: its first line is the commit title and the
	// rest the commit body. Empty leaves the SCM's default message.
	// +kubebuilder:validation:MaxLength=4096
	// +optional
	CommitMessageTemplate string `json:"commitMessageTemplate,omitempty"`
}

// PromotionTemplateRef is the shape of the deprecated
// spec.environments[].promotionTemplate field. The PromotionTemplate CRD it
// named was removed; a Pipeline that sets the field is rejected.
type PromotionTemplateRef struct {
	// Name is the name of a PromotionTemplate (the CRD was removed).
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace is the namespace of the PromotionTemplate (the CRD was removed).
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// AutoRollbackSpec is reserved for a consecutive-failure rollback policy. It is
// not implemented, and EnvironmentSpec rejects it; see OnHealthFailure.
type AutoRollbackSpec struct {
	// FailureThreshold is the number of consecutive health-check failures
	// that trigger an automatic rollback Bundle creation. Default: 3.
	// +kubebuilder:default=3
	// +optional
	FailureThreshold int `json:"failureThreshold,omitempty"`
}

// BakeConfig defines a contiguous-healthy soak window for an environment (K-01).
// The health check must pass continuously for Minutes before the step is Verified.
type BakeConfig struct {
	// Minutes is the required contiguous healthy duration in minutes.
	// The window restarts at the next healthy check after a check that is not
	// healthy (see Policy).
	// +kubebuilder:validation:Minimum=1
	Minutes int `json:"minutes"`

	// Policy controls what an unhealthy check during the bake window does.
	// "reset-on-alarm" (default): the window stops, status.bakeResets increments
	// and the step stays in HealthChecking.
	// "fail-on-alarm": onHealthFailure applies at the first unhealthy check.
	// A waiting check (the workload is changing, such as a canary paused at a
	// step) stops the window under either policy but is not an alarm. A
	// stopped window restarts at the next healthy check, and health.timeout
	// bounds the wait for it.
	// +kubebuilder:validation:Enum=reset-on-alarm;fail-on-alarm
	// +kubebuilder:default=reset-on-alarm
	// +optional
	Policy string `json:"policy,omitempty"`

	// MaxDuration bounds the time from the first bake window's start
	// (status.bakeFirstStartedAt) to a complete window. A step that has not
	// completed one full window by then applies onHealthFailure when its
	// window stops, also on a Waiting check, so a release that keeps
	// flapping under reset-on-alarm ends. Go duration format (e.g. "36h").
	// Default: minutes + health.timeout. A value shorter than minutes counts
	// as minutes.
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	MaxDuration string `json:"maxDuration,omitempty"`
}

// WebhookConfig is the shape of the deprecated spec.environments[].steps[].webhook
// field. The custom webhook step was removed; nothing calls this endpoint.
type WebhookConfig struct {
	// URL is the HTTP(S) endpoint to POST to.
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// TimeoutSeconds is the per-call timeout. Defaults to 300.
	// +kubebuilder:default=300
	// +optional
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`

	// SecretRef references a Kubernetes Secret whose "Authorization" key is
	// sent as the Authorization header.
	// +optional
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// StepSpec is the shape of the deprecated spec.environments[].steps field. A
// Pipeline that sets steps is rejected; see EnvironmentSpec.Steps.
type StepSpec struct {
	// Uses names a step.
	// +kubebuilder:validation:MinLength=1
	Uses string `json:"uses"`

	// Webhook was the endpoint of a custom webhook step, which was removed.
	// +optional
	Webhook *WebhookConfig `json:"webhook,omitempty"`
}

// UpdateConfig holds manifest update strategy configuration.
type UpdateConfig struct {
	// Strategy selects the manifest update strategy.
	// +kubebuilder:validation:Enum=kustomize;helm;argocd
	// +kubebuilder:default=kustomize
	// +optional
	Strategy string `json:"strategy,omitempty"`

	// Helm holds Helm-specific update configuration.
	// Used when Strategy is "helm".
	// +optional
	Helm *HelmUpdateConfig `json:"helm,omitempty"`

	// ArgoCD holds ArgoCD-native update configuration.
	// Used when Strategy is "argocd". Patches the ArgoCD Application's
	// spec.source.helm.valuesObject directly without a git commit.
	// +optional
	ArgoCD *ArgoCDUpdateConfig `json:"argocd,omitempty"`
}

// HelmUpdateConfig holds Helm-specific update strategy configuration.
type HelmUpdateConfig struct {
	// ImagePathTemplate is the YAML dot-path to the image tag in values.yaml.
	// Example: ".image.tag" updates the `image.tag` key.
	// If empty, defaults to ".image.tag".
	// +optional
	ImagePathTemplate string `json:"imagePathTemplate,omitempty"`

	// ValuesFile is the name of the values file to update (relative to the
	// environment path). Defaults to "values.yaml".
	// +optional
	ValuesFile string `json:"valuesFile,omitempty"`
}

// ArgoCDUpdateConfig holds ArgoCD-native update strategy configuration.
// Used when UpdateConfig.Strategy is "argocd".
// The argocd-set-image step patches the ArgoCD Application's
// spec.source.helm.valuesObject in-place, unlocking teams that store
// application config inside an ArgoCD Application rather than a GitOps repo.
type ArgoCDUpdateConfig struct {
	// Application is the name of the ArgoCD Application resource to patch.
	// +kubebuilder:validation:MinLength=1
	Application string `json:"application"`

	// Namespace is the Kubernetes namespace where the ArgoCD Application lives.
	// Defaults to "argocd" if empty.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// ImageKey is the dot-separated key path within spec.source.helm.valuesObject
	// where the image tag should be written.
	// Example: "image.tag" writes to spec.source.helm.valuesObject.image.tag.
	// Defaults to "image.tag" if empty.
	// +optional
	ImageKey string `json:"imageKey,omitempty"`
}

// HealthConfig holds health check configuration for an environment.
type HealthConfig struct {
	// Type selects the health check backend.
	// Supported values: resource, argocd, flux, argoRollouts, flagger.
	// When empty the PromotionStep reconciler uses "resource" (a Deployment named
	// after the Pipeline in the environment namespace, unless health.resource
	// overrides it). delivery.delegate, when set, takes precedence.
	// +kubebuilder:validation:Enum=resource;argocd;flux;argoRollouts;flagger
	// +optional
	Type string `json:"type,omitempty"`

	// Timeout is the maximum time to wait for a healthy check: from the start
	// of health checking, and again whenever a bake window stops. When it
	// expires, onHealthFailure applies. It does not cut a running bake window
	// short. Uses Go duration format (e.g. "30m", "1h"). Defaults to "10m".
	// +kubebuilder:validation:Pattern=`^$|^(0|(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))+)$`
	// +optional
	Timeout string `json:"timeout,omitempty"`

	// Cluster is not supported: kardinal checks health only in the cluster it
	// runs in. A non-empty value sets the Pipeline Ready=False (reason
	// NotImplemented) and fails the PromotionStep with "health.cluster is not
	// supported" instead of silently checking the local cluster.
	//
	// Deprecated: remove cluster. To verify a workload in another cluster,
	// check its Argo CD Application (health.type: argocd) or Flux
	// Kustomization (health.type: flux) in the hub cluster kardinal runs in;
	// see docs/health-adapters.md#remote-clusters.
	// +optional
	Cluster string `json:"cluster,omitempty"`

	// LabelSelector enables WatchKind mode for health.type=resource.
	// When set, the health node watches ALL Deployments in the environment namespace
	// that match the given labels (a kro Graph collection ref node).
	// When unset, a single named Deployment is watched (a kro Graph ref node).
	//
	// Example: {"app": "my-service", "kardinal.io/pipeline": "nginx-demo"}
	//
	// Only applies to health.type=resource. Ignored for argocd, flux, argoRollouts, flagger
	// (those resource types are always single-named).
	// +optional
	LabelSelector map[string]string `json:"labelSelector,omitempty"`

	// Resource specifies the exact Kubernetes resource to watch for health.type=resource.
	// When set, overrides the default behavior (which watches a Deployment named after
	// the pipeline in the environment namespace). Use this when the health target is
	// in a different namespace or has a different name than the pipeline.
	//
	// Only applies to health.type=resource. Ignored for argocd, flux, argoRollouts, flagger.
	// +optional
	Resource *ResourceRef `json:"resource,omitempty"`

	// ArgoCD overrides the Argo CD Application checked by health.type=argocd.
	// Defaults: name "<pipeline>-<environment>", namespace "argocd".
	// +optional
	ArgoCD *HealthTargetRef `json:"argocd,omitempty"`

	// Flux overrides the Flux Kustomization checked by health.type=flux.
	// Defaults: name "<pipeline>-<environment>", namespace "flux-system".
	// +optional
	Flux *HealthTargetRef `json:"flux,omitempty"`

	// ArgoRollouts overrides the Rollout checked by health.type=argoRollouts
	// (or delivery.delegate=argoRollouts).
	// Defaults: name "<pipeline>", namespace "<environment>".
	// +optional
	ArgoRollouts *HealthTargetRef `json:"argoRollouts,omitempty"`

	// Flagger overrides the Canary checked by health.type=flagger
	// (or delivery.delegate=flagger).
	// Defaults: name "<pipeline>", namespace "<environment>".
	// +optional
	Flagger *HealthTargetRef `json:"flagger,omitempty"`
}

// HealthTargetRef names the object a health adapter reads.
// Empty fields fall back to the adapter's default.
type HealthTargetRef struct {
	// Name is the object name.
	// +optional
	Name string `json:"name,omitempty"`

	// Namespace is the object namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ResourceRef identifies a Kubernetes resource by kind, name, and namespace.
type ResourceRef struct {
	// Kind is the Kubernetes resource kind. Only "Deployment" is supported;
	// any other value fails the PromotionStep with a clear message.
	// Defaults to "Deployment" when unset.
	// +optional
	Kind string `json:"kind,omitempty"`

	// Condition is the Deployment condition type that must be True.
	// Defaults to "Available".
	// +optional
	Condition string `json:"condition,omitempty"`

	// Name is the resource name. Defaults to the pipeline name when unset.
	// +optional
	Name string `json:"name,omitempty"`

	// Namespace is the resource namespace. Defaults to the environment name when unset.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// DeliveryConfig holds in-cluster progressive delivery delegation configuration.
type DeliveryConfig struct {
	// Delegate offloads in-cluster progressive delivery to an external controller.
	// Supported values: none, argoRollouts, flagger.
	// +kubebuilder:validation:Enum=none;argoRollouts;flagger
	// +optional
	Delegate string `json:"delegate,omitempty"`
}

// PipelinePolicyGateRef is a reference to a PolicyGate that must pass before
// any promotion in this pipeline can proceed.
type PipelinePolicyGateRef struct {
	// Name is the PolicyGate resource name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace is the PolicyGate resource namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// PipelineStatus defines the observed state of a Pipeline.
type PipelineStatus struct {
	// Phase is the overall pipeline phase: Promoting while a Bundle is in
	// flight (also when a PolicyGate holds it), Degraded when the newest Bundle
	// failed, Ready when the newest Bundle is Verified in every environment it
	// reached, and Unknown before the first Bundle.
	// +kubebuilder:validation:Enum=Ready;Degraded;Promoting;Unknown
	// +kubebuilder:default=Unknown
	Phase string `json:"phase,omitempty"`

	// Conditions holds status conditions.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DeploymentMetrics holds aggregate DORA-style metrics computed from the
	// last 30 Verified Bundles for this Pipeline. Written by PipelineReconciler.
	// +optional
	DeploymentMetrics *PipelineDeploymentMetrics `json:"deploymentMetrics,omitempty"`
}

// PipelineDeploymentMetrics holds aggregate promotion efficiency metrics for a Pipeline.
// Computed by PipelineReconciler from the last 30 Verified Bundles for this pipeline.
// Displayed by `kardinal metrics` and the UI pipeline detail view.
type PipelineDeploymentMetrics struct {
	// RolloutsLast30Days is the number of successful (Verified) promotions to
	// the final pipeline environment in the last 30 calendar days.
	// +optional
	RolloutsLast30Days int `json:"rolloutsLast30Days,omitempty"`

	// P50CommitToProdMinutes is the median time (minutes) from Bundle creation
	// to the final environment reaching Verified, over the sample window.
	// +optional
	P50CommitToProdMinutes int64 `json:"p50CommitToProdMinutes,omitempty"`

	// P90CommitToProdMinutes is the 90th-percentile time (minutes) from Bundle
	// creation to the final environment reaching Verified, over the sample window.
	// +optional
	P90CommitToProdMinutes int64 `json:"p90CommitToProdMinutes,omitempty"`

	// AutoRollbackRateMillis is the fraction of sampled Bundles that are
	// rollbacks (spec.provenance.rollbackOf is set), manual (`kardinal
	// rollback`, the UI) or automatic, expressed as integer thousandths
	// (e.g. 83 = 8.3%).
	// Stored as integer to avoid floating-point in CRD YAML.
	// +optional
	AutoRollbackRateMillis int `json:"autoRollbackRateMillis,omitempty"`

	// OperatorInterventionRateMillis is the fraction of sampled Bundles that had
	// at least one PolicyGate override applied, expressed as integer thousandths.
	// +optional
	OperatorInterventionRateMillis int `json:"operatorInterventionRateMillis,omitempty"`

	// StaleProdDays is the number of days since the last successful promotion to
	// the final pipeline environment. 0 means a promotion completed today.
	// Until a Bundle is Verified there, deploymentMetrics is not set at all.
	// +optional
	StaleProdDays int `json:"staleProdDays,omitempty"`

	// SampleSize is the number of Bundles included in this computation.
	// +optional
	SampleSize int `json:"sampleSize,omitempty"`

	// ComputedAt is when these metrics were last written by the PipelineReconciler.
	// +optional
	ComputedAt *metav1.Time `json:"computedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=pipe
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Paused",type=boolean,JSONPath=`.spec.paused`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Pipeline defines a promotion pipeline for one application.
// It specifies the ordered environments an artifact Bundle travels through.
type Pipeline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PipelineSpec   `json:"spec,omitempty"`
	Status PipelineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PipelineList contains a list of Pipeline.
type PipelineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Pipeline `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Pipeline{}, &PipelineList{})
}
