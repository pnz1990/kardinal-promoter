// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package main is the entry point for the kardinal-controller binary.
// The controller watches Pipeline, Bundle, PolicyGate, and PromotionStep CRDs
// and drives promotion workflows.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	sigroot "github.com/sigstore/sigstore-go/pkg/root"
	sigtuf "github.com/sigstore/sigstore-go/pkg/tuf"
	tuffetcher "github.com/theupdateframework/go-tuf/v2/metadata/fetcher"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	czap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/accesslog"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
	graphpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	healthpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/auditretention"
	bundlereconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	changewindowrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/graphcleanup"
	hookrunrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/hookrun"
	ivrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification"
	metriccheckrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/metriccheck"
	nhookrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/notificationhook"
	pipelinereconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
	policygaterecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
	psreconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	prstatusrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
	rbprecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/rollbackpolicy"
	scheduleclockrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/scheduleclock"
	scmproviderrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/scmprovider"
	subscriptionrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/subscription"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
	"github.com/kardinal-promoter/kardinal-promoter/web"

	// Import built-in steps to register them via init().
	_ "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"

	// Embed the IANA timezone database. The runtime image has no tzdata, and
	// ChangeWindow spec.schedule.timezone ("America/Los_Angeles") needs it:
	// without it every named timezone is invalid and the window always blocks.
	// TestControllerEmbedsTZData guards this import.
	_ "time/tzdata"
)

// ControllerVersion is the controller version string, overridable at build time via ldflags.
// Set to the same value as the CLI version tag during release builds.
//
//nolint:gochecknoglobals // Build-time constant, analogous to cmd/kardinal/cmd.CLIVersion.
var ControllerVersion = "v0.1.0-dev"

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kardinalv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		leaderElect            bool
		zerologLevel           string
		metricsBindAddress     string
		healthProbeBindAddress string
		webhookBindAddress     string
		policyNamespaces       string
		githubToken            string
		webhookSecret          string
		scmProviderType        string
		scmAPIURL              string
		gateStatusHeartbeat    time.Duration
		auditRetention         bool
		auditMaxAge            time.Duration
		auditMaxPerPipeline    int
		auditRetentionInterval time.Duration
		workers                = map[string]*int{}
		graphCompactAbove      int
		retire                 bundlereconciler.RetirePolicy
	)

	var scmWaitTimeout time.Duration
	flag.DurationVar(&scmWaitTimeout, "scm-wait-timeout", psreconciler.DefaultSCMWaitTimeout,
		"Longest a PromotionStep waits for an open SCM circuit (its SCM host keeps failing) before it fails, "+
			"when its environment sets no stepTimeoutSeconds.")
	flag.BoolVar(&auditRetention, "audit-retention", false,
		"Delete AuditEvents past their retention (--audit-retention-max-age, --audit-retention-max-per-pipeline). "+
			"Off by default: every record is kept until you opt in.")
	flag.DurationVar(&auditMaxAge, "audit-retention-max-age", auditretention.DefaultMaxAge,
		"Delete AuditEvents created (metadata.creationTimestamp) longer ago than this. 0 keeps records of any age.")
	flag.IntVar(&auditMaxPerPipeline, "audit-retention-max-per-pipeline", auditretention.DefaultMaxPerPipeline,
		"Keep at most this many newest AuditEvents per Pipeline. 0 keeps any number.")
	flag.DurationVar(&auditRetentionInterval, "audit-retention-interval", auditretention.DefaultInterval,
		"How often the leader applies AuditEvent retention.")
	// Workers per controller: one object is never reconciled twice at once
	// (the work queue serializes it), so these only let different objects
	// run side by side. The defaults are measured with the scale suite
	// (docs/installation.md, Controller concurrency).
	for _, w := range []struct {
		name string
		def  int
		what string
	}{
		{"promotionstep", defaultPromotionStepWorkers, "PromotionSteps (git clone, commit, push, PR, health check)"},
		{"bundle", defaultBundleWorkers, "Bundles (Graph creation)"},
		{"prstatus", defaultPRStatusWorkers, "PRStatuses (SCM polls)"},
		{"policygate", defaultPolicyGateWorkers, "PolicyGates (CEL evaluation)"},
		{"pipeline", defaultPipelineWorkers, "Pipelines (status, history)"},
	} {
		v := new(int)
		workers[w.name] = v
		flag.IntVar(v, w.name+"-workers", w.def, "How many "+w.what+" are reconciled at once.")
	}

	var gateOverrideMaxMinutes int
	flag.IntVar(&gateOverrideMaxMinutes, "gate-override-max-minutes", int(policygaterecon.DefaultMaxOverride.Minutes()),
		"Longest a gate override counts, from when the controller first saw it; an override ends at the earlier of "+
			"its expiresAt and this cap (Helm gates.overrideMaxMinutes).")
	var overrideIdentityPolicy string
	flag.StringVar(&overrideIdentityPolicy, "override-identity-policy", "",
		"Name of the chart's gate-overrides ValidatingAdmissionPolicy and binding. The controller records an "+
			"override's createdBy as verified only while both exist; empty records every override unverified.")
	flag.DurationVar(&gateStatusHeartbeat, "gate-status-heartbeat", policygaterecon.DefaultStatusHeartbeat,
		"Longest a PolicyGate's status goes unwritten while its result does not change. Each status write makes kro "+
			"re-check the gate's whole Graph. 0 writes the status on every evaluation.")
	flag.IntVar(&graphCompactAbove, "graph-compact-above", graphpkg.DefaultCompactAbove,
		"Environment count above which a Bundle's Graph uses the compact shape (one PromotionStep collection) "+
			"when the Pipeline's kardinal.io/graph-shape annotation does not choose one. 0 makes every Graph compact.")
	flag.DurationVar(&retire.Superseded, "graph-retire-superseded-after", bundlereconciler.DefaultRetirePolicy.Superseded,
		"How long the Graph of a Superseded Bundle, or of a Verified one a newer Bundle replaced in every environment, "+
			"is kept before it is retired (deleted; the Bundle keeps its PromotionSteps in status.retiredSteps). 0 keeps it.")
	flag.DurationVar(&retire.Failed, "graph-retire-failed-after", bundlereconciler.DefaultRetirePolicy.Failed,
		"How long the Graph of a Failed Bundle is kept before it is retired. A retired Failed Bundle no longer recovers. 0 keeps it.")
	flag.DurationVar(&retire.Verified, "graph-retire-verified-after", bundlereconciler.DefaultRetirePolicy.Verified,
		"How long the Graph of a Verified Bundle that is still deployed in an environment is kept before it is retired. 0 keeps it.")
	flag.BoolVar(&leaderElect, "leader-elect", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&zerologLevel, "log-level", "info",
		"Log level for zerolog. One of: debug, info, warn, error.")
	flag.StringVar(&metricsBindAddress, "metrics-bind-address", ":8080",
		"The address the metric endpoint binds to.")
	flag.StringVar(&healthProbeBindAddress, "health-probe-bind-address", ":8081",
		"The address the probe endpoint binds to.")
	flag.StringVar(&webhookBindAddress, "webhook-bind-address", ":8083",
		"The address the SCM webhook endpoint binds to.")
	flag.StringVar(&policyNamespaces, "policy-namespaces", "platform-policies",
		"Comma-separated list of namespaces to scan for org-level PolicyGates.")
	flag.StringVar(&githubToken, "github-token", os.Getenv("GITHUB_TOKEN"),
		"SCM token for API operations (a GitHub, GitLab, Forgejo/Gitea, Bitbucket or Azure DevOps token).")
	flag.StringVar(&webhookSecret, "webhook-secret", os.Getenv("KARDINAL_WEBHOOK_SECRET"),
		"Secret for validating incoming SCM webhooks (an HMAC key or a shared token, depending on the provider; see docs/scm-providers.md).")
	flag.StringVar(&scmProviderType, "scm-provider", os.Getenv("KARDINAL_SCM_PROVIDER"),
		"SCM provider type for the whole controller: \"github\" (default), \"gitlab\", \"forgejo\", \"gitea\", \"bitbucket\" or \"azuredevops\".")
	flag.StringVar(&scmAPIURL, "scm-api-url", os.Getenv("KARDINAL_SCM_API_URL"),
		"SCM API base URL override (e.g. for GitHub Enterprise or self-managed GitLab).")
	var scmAllowedRepositories string
	flag.StringVar(&scmAllowedRepositories, "scm-allowed-repositories", os.Getenv("KARDINAL_SCM_ALLOWED_REPOSITORIES"),
		"Comma-separated host/repository globs (github.com/acme/*, gitlab.example.com/team/**) of the "+
			"repositories the controller's SCM token may act on. Every SCM call for another repository is "+
			"refused, and a Pipeline that would need the token for one is Ready=False/RepositoryNotAllowed "+
			"and its steps fail. Empty allows every repository.")

	gatesCommitStatus := true
	flag.BoolVar(&gatesCommitStatus, "gates-commit-status", true,
		"Post the gate results of a waiting pr-review step as a commit status on its PR (Helm "+
			"scm.gatesCommitStatus.enabled). false posts none, for a token without the commit-status permission.")
	var gatesStatusContext string
	flag.StringVar(&gatesStatusContext, "gates-status-context", scm.GatesStatusContext,
		"Commit status context (GitLab name, Bitbucket key, Azure DevOps genre/name) the gate results are "+
			"posted under (Helm scm.gatesCommitStatus.context). Reserved for kardinal: branch protection "+
			"requires it, so nothing else may post under it.")

	var bundleToken string
	flag.StringVar(&bundleToken, "bundle-api-token", os.Getenv("KARDINAL_BUNDLE_TOKEN"),
		"Bearer token for authenticating POST /api/v1/bundles requests.")

	var uiAuthToken string
	flag.StringVar(&uiAuthToken, "ui-auth-token", os.Getenv("KARDINAL_UI_TOKEN"),
		"Bearer token for authenticating /api/v1/ui/* requests. "+
			"When set, all UI API routes require 'Authorization: Bearer <token>'. "+
			"When empty (default) and --ui-tokenreview-auth is off, /api/ answers only "+
			"loopback clients (kubectl port-forward) and refuses the rest with 403. "+
			"Also readable from KARDINAL_UI_TOKEN environment variable.")

	var uiListenAddress string
	flag.StringVar(&uiListenAddress, "ui-listen-address", ":8082",
		"The address the embedded kardinal-ui HTTP server binds to.")

	var corsAllowedOrigins string
	flag.StringVar(&corsAllowedOrigins, "cors-allowed-origins", os.Getenv("KARDINAL_CORS_ORIGINS"),
		"Comma-separated list of allowed CORS origins for /api/v1/ui/* routes. "+
			"Default (empty): same-origin only — cross-origin requests are rejected with 403. "+
			"Set to '*' to allow all origins (development only). "+
			"Also readable from KARDINAL_CORS_ORIGINS environment variable.")

	var accessLogAll, accessLogSourceIP bool
	var accessLogTrustedProxies string
	flag.BoolVar(&accessLogAll, "access-log-all-requests", os.Getenv("KARDINAL_ACCESS_LOG_ALL_REQUESTS") == "true",
		"Log every UI API and Bundle API request, not only logins (TokenReviews), refusals (401/403/429) and "+
			"writes. Chart value: controller.accessLog.allRequests.")
	flag.BoolVar(&accessLogSourceIP, "access-log-source-ip", os.Getenv("KARDINAL_ACCESS_LOG_SOURCE_IP") == "true",
		"Add the client address to each access log line. Chart value: controller.accessLog.sourceIP.")
	flag.StringVar(&accessLogTrustedProxies, "access-log-trusted-proxies", os.Getenv("KARDINAL_ACCESS_LOG_TRUSTED_PROXIES"),
		"Comma-separated CIDRs of proxies (an Ingress controller) whose X-Forwarded-For gives the client address "+
			"in the access log. Chart value: controller.accessLog.trustedProxies.")

	var uiAllowedHosts string
	flag.StringVar(&uiAllowedHosts, "ui-allowed-hosts", os.Getenv("KARDINAL_UI_ALLOWED_HOSTS"),
		"Comma-separated host names (no scheme; a port is ignored) the UI server answers to, "+
			"on top of localhost, 127.0.0.1 and ::1: the controller Service DNS names and any Ingress host. "+
			"A request counts as same-origin only when its Host header is one of these, and while UI auth is "+
			"off, every /api/ request with any other Host is rejected with 403, reads included (DNS rebinding "+
			"protection). Static /ui/ assets are not checked. Chart value: ui.allowedHosts. "+
			"Also readable from KARDINAL_UI_ALLOWED_HOSTS environment variable.")

	// --ui-tokenreview-auth enables Kubernetes TokenReview-based authentication for
	// the UI API. When set to true (and --ui-auth-token is NOT set), the UI API server
	// validates each bearer token by calling authenticationv1.TokenReview against the
	// Kubernetes API server. This allows cluster users to access the UI with their
	// existing kubeconfig credentials — no shared static secret to leak.
	//
	// Priority (O4): --ui-auth-token takes precedence. If both are set, only the static
	// token check is applied and TokenReview is not called.
	//
	// Design ref: docs/design/15-production-readiness.md §Lens 4
	var uiTokenReviewAuth bool
	flag.BoolVar(&uiTokenReviewAuth, "ui-tokenreview-auth",
		os.Getenv("KARDINAL_UI_TOKENREVIEW_AUTH") == "true",
		"Enable Kubernetes TokenReview-based authentication for /api/v1/ui/* routes. "+
			"When true and --ui-auth-token is not set, each request's bearer token is "+
			"validated via authenticationv1.TokenReview. Fail-closed: API errors return 503. "+
			"Also readable from KARDINAL_UI_TOKENREVIEW_AUTH environment variable (set to 'true').")

	// --metriccheck-cloudwatch-ambient-credentials lets cloudwatch MetricChecks
	// that name no credential Secret use the controller's own AWS identity
	// (IRSA, EKS Pod Identity, environment). Off by default: any user who can
	// create a MetricCheck could read CloudWatch with that identity.
	var cloudWatchAmbient bool
	flag.BoolVar(&cloudWatchAmbient, "metriccheck-cloudwatch-ambient-credentials", false,
		"Let cloudwatch MetricChecks without credential Secret refs use the controller's own AWS identity "+
			"(SDK default chain: environment, IRSA, EKS Pod Identity). Off by default.")

	// --metriccheck-global-slots and --metriccheck-namespace-slots cap the
	// outbound MetricCheck queries (metriccheckrecon.Limiter).
	var metricGlobalSlots, metricNamespaceSlots int
	flag.IntVar(&metricGlobalSlots, "metriccheck-global-slots", metriccheckrecon.DefaultGlobalSlots,
		"Most MetricCheck queries running at once in the cluster (at least 1). The rest wait, first come, first served. "+
			"The MetricCheck controller runs this many workers plus 4, at least 16.")
	flag.IntVar(&metricNamespaceSlots, "metriccheck-namespace-slots", metriccheckrecon.DefaultNamespaceSlots,
		"Most MetricCheck queries of one namespace running at once (at least 1).")

	var tlsCertFile string
	flag.StringVar(&tlsCertFile, "tls-cert-file", os.Getenv("KARDINAL_TLS_CERT_FILE"),
		"Path to the TLS certificate file (PEM). When set together with --tls-key-file, "+
			"both the UI and webhook servers use HTTPS instead of plain HTTP. "+
			"Also readable from KARDINAL_TLS_CERT_FILE environment variable.")

	var tlsKeyFile string
	flag.StringVar(&tlsKeyFile, "tls-key-file", os.Getenv("KARDINAL_TLS_KEY_FILE"),
		"Path to the TLS private key file (PEM). When set together with --tls-cert-file, "+
			"both the UI and webhook servers use HTTPS instead of plain HTTP. "+
			"Also readable from KARDINAL_TLS_KEY_FILE environment variable.")

	// --shard and --pipeline-admission-webhook were removed; setting one stops
	// the controller with a message (removed_settings.go).
	removed := bindRemovedFlags(flag.CommandLine, os.Getenv)

	// SCM credential rotation — watch a Kubernetes Secret and reload the SCM
	// provider on change without restarting the controller. When
	// --scm-token-secret-name is set, the --github-token flag is used only as
	// the initial value (bootstrapping) and the Secret becomes the authoritative
	// source thereafter.
	var scmProvidersAllowHTTP bool
	flag.BoolVar(&scmProvidersAllowHTTP, "scm-providers-allow-http", false,
		"Let ScmProviders and ClusterScmProviders use an http:// spec.apiURL, for an in-cluster SCM without TLS. "+
			"Off, a provider must use https://, so its token never crosses the network in clear text.")

	var scmTokenSecretName string
	flag.StringVar(&scmTokenSecretName, "scm-token-secret-name",
		os.Getenv("KARDINAL_SCM_TOKEN_SECRET_NAME"),
		"Name of a Kubernetes Secret whose data key contains the SCM token. "+
			"When set, the controller watches this Secret and reloads the SCM provider "+
			"on token change — no restart required (zero-downtime credential rotation). "+
			"Also readable from KARDINAL_SCM_TOKEN_SECRET_NAME environment variable.")

	var scmTokenSecretNamespace string
	flag.StringVar(&scmTokenSecretNamespace, "scm-token-secret-namespace",
		os.Getenv("KARDINAL_SCM_TOKEN_SECRET_NAMESPACE"),
		"Namespace of the Secret named by --scm-token-secret-name. "+
			"Defaults to the POD_NAMESPACE environment variable, then 'kardinal-system'. "+
			"Also readable from KARDINAL_SCM_TOKEN_SECRET_NAMESPACE environment variable.")

	var scmTokenSecretKey string
	flag.StringVar(&scmTokenSecretKey, "scm-token-secret-key",
		os.Getenv("KARDINAL_SCM_TOKEN_SECRET_KEY"),
		"Data key within the Secret that holds the SCM token (default: \"token\"). "+
			"Also readable from KARDINAL_SCM_TOKEN_SECRET_KEY environment variable.")

	// --watch-namespace limits the controller's informer cache to a single namespace.
	// When empty (default), the controller watches all namespaces (cluster-wide mode).
	// When set, the controller watches only that namespace — suitable for multi-tenant
	// clusters where a ClusterRole with cluster-wide access is not acceptable.
	// Also readable from KARDINAL_WATCH_NAMESPACE environment variable.
	// Design ref: docs/design/15-production-readiness.md §Lens 6
	var watchNamespace string
	flag.StringVar(&watchNamespace, "watch-namespace",
		os.Getenv("KARDINAL_WATCH_NAMESPACE"),
		"Namespace to watch (default: \"\" = cluster-wide). When set, the controller "+
			"only reconciles resources in the given namespace and expects a Role/RoleBinding "+
			"instead of a ClusterRole/ClusterRoleBinding. "+
			"Also readable from KARDINAL_WATCH_NAMESPACE environment variable.")

	// --namespace-shard splits the reconcilers across controller
	// installations by the namespace label kardinal.io/shard (pkg/shard).
	var namespaceShard string
	flag.StringVar(&namespaceShard, "namespace-shard", os.Getenv("KARDINAL_NAMESPACE_SHARD"),
		"Shard this controller reconciles: namespaces labelled kardinal.io/shard=<name>; \"default\" also "+
			"takes namespaces without the label and the cluster-scoped kinds. Empty (the default): no sharding, "+
			"every namespace. Every controller of a sharded cluster needs a shard name. "+
			"Also readable from KARDINAL_NAMESPACE_SHARD.")

	// Graph identity: kro applies each Graph as this ServiceAccount in the
	// Pipeline's namespace. The controller creates it and binds it to the two
	// ClusterRoles the chart ships (templates/graph-rbac.yaml). The translator,
	// the Graph cleanup reconciler and the reader binding sweep share it (and
	// its lock).
	graphIdentity := &graphpkg.IdentityProvisioner{}
	flag.StringVar(&graphIdentity.ServiceAccountName, "graph-service-account",
		graphpkg.DefaultGraphServiceAccount,
		"ServiceAccount (created in each Pipeline namespace) that kro impersonates to apply Graphs.")
	flag.StringVar(&graphIdentity.ApplierClusterRole, "graph-applier-clusterrole",
		graphpkg.DefaultApplierClusterRole,
		"ClusterRole bound to the Graph ServiceAccount in the Pipeline namespace.")
	flag.StringVar(&graphIdentity.ReaderClusterRole, "graph-reader-clusterrole",
		graphpkg.DefaultReaderClusterRole,
		"ClusterRole bound to the Graph ServiceAccount in namespaces its health checks read.")
	var graphReaderNamespaces string
	flag.StringVar(&graphReaderNamespaces, "graph-reader-namespaces",
		strings.Join(graphpkg.DefaultReaderNamespaces, ","),
		"Comma-separated namespaces, besides a Graph's own namespace, where the Graph ServiceAccount "+
			"may be bound to the reader ClusterRole for health checks. \"*\" allows every namespace "+
			"except kube-system, kube-public and kube-node-lease. Health checks in other namespaces "+
			"get no Graph ref.")

	var scmInstanceSigners string
	flag.StringVar(&scmInstanceSigners, "scm-instance-signers", "",
		"Comma-separated names or emails the Forgejo/Gitea instance signs commits with (repository.signing "+
			"SIGNING_NAME / SIGNING_EMAIL). Image verification treats a commit signed by one as a platform "+
			"signature (forgejo-instance), refused unless commits.allowedSigners lists forgejo-instance. Without "+
			"it, a verified signer that is not a user of the instance is the instance key.")

	var hookServiceAccounts string
	flag.StringVar(&hookServiceAccounts, "hook-service-accounts", hookrunrecon.DefaultServiceAccount,
		"Comma-separated ServiceAccount names a Pipeline hook's Job Pod may run as (in the Pipeline "+
			"namespace). A hook whose Pod names another ServiceAccount fails without running. The Graph "+
			"ServiceAccount (--graph-service-account) is never allowed. See docs/hooks.md.")

	var hookPodSecurityLevel string
	flag.StringVar(&hookPodSecurityLevel, "hook-pod-security-level", hookrunrecon.DefaultPodSecurityLevel,
		"Pod Security Standard a hook Job Pod must meet: baseline (default), restricted or privileged "+
			"(no Pod checks). Below privileged, nodeName and hostPort are refused too. A hook that breaks it fails "+
			"without running. See docs/hooks.md.")

	var tracingCfg tracing.Config
	flag.BoolVar(&tracingCfg.Enabled, "tracing-enabled", os.Getenv("KARDINAL_TRACING_ENABLED") == "true",
		"Export OpenTelemetry traces over OTLP/HTTP: a span per reconcile, promotion step, git clone and push, "+
			"SCM API request and NotificationHook delivery, and server spans for /webhook/scm and /api/v1/bundles. "+
			"Off by default. Chart value: tracing.enabled. Also readable from KARDINAL_TRACING_ENABLED.")
	flag.StringVar(&tracingCfg.Endpoint, "tracing-endpoint", os.Getenv("KARDINAL_TRACING_ENDPOINT"),
		"OTLP/HTTP endpoint: a URL (http://otel-collector.observability:4318; /v1/traces is added) or host:port. "+
			"Empty uses OTEL_EXPORTER_OTLP_TRACES_ENDPOINT or OTEL_EXPORTER_OTLP_ENDPOINT, else localhost:4318. "+
			"Chart value: tracing.endpoint.")
	flag.BoolVar(&tracingCfg.Insecure, "tracing-insecure", os.Getenv("KARDINAL_TRACING_INSECURE") == "true",
		"Send traces over plain HTTP to a host:port --tracing-endpoint. Chart value: tracing.insecure.")
	flag.Float64Var(&tracingCfg.SamplingRatio, "tracing-sampling-ratio", 0.1,
		"Fraction of traces recorded, 0 to 1, decided once per trace at its root (a reconcile, or an inbound "+
			"request: an inbound traceparent is linked, not trusted, so it does not force sampling). "+
			"Chart value: tracing.samplingRatio.")

	var egressAllowlist string
	flag.StringVar(&egressAllowlist, "egress-allowlist", os.Getenv("KARDINAL_EGRESS_ALLOWLIST"),
		"Comma-separated destinations NotificationHook, MetricCheck and Subscription requests may reach: "+
			"host names (hooks.slack.com), wildcards (*.example.com, any name under it) and CIDRs or addresses "+
			"(10.0.0.0/8, 10.1.2.3). A request is allowed when its host matches a name entry or every address "+
			"it connects to is in a CIDR entry. Empty (the default): every destination except loopback, "+
			"link-local, cloud metadata, unspecified and multicast addresses, which stay refused whatever "+
			"this lists. Chart value: egress.allowlist. Also readable from KARDINAL_EGRESS_ALLOWLIST.")

	// controller-runtime uses its own flag set; parse standard flags here
	opts := czap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	graphIdentity.ReaderNamespaces = splitCSV(graphReaderNamespaces)
	graphIdentity.OnlyNamespace = watchNamespace

	// Configure zerolog level
	level, err := zerolog.ParseLevel(zerologLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	if metricGlobalSlots < 1 || metricNamespaceSlots < 1 {
		logger.Fatal().Int("globalSlots", metricGlobalSlots).Int("namespaceSlots", metricNamespaceSlots).
			Msg("--metriccheck-global-slots and --metriccheck-namespace-slots must be at least 1")
	}
	// Reconcilers log through zerolog.Ctx(ctx). controller-runtime does not put a
	// zerolog logger in the reconcile context, so without this default every
	// reconciler line, errors included, goes to a disabled logger.
	zerolog.DefaultContextLogger = &logger

	if err := removed.err(); err != nil {
		logger.Fatal().Err(err).Msg("a removed controller setting is still set")
	}

	ctrl.SetLogger(czap.New(czap.UseFlagOptions(&opts)))

	// The Go runtime's soft memory limit at 90% of the container limit, so
	// the GC works harder before the kernel OOMKills the controller (#1553).
	if limit, why, err := applyGoMemoryLimit(os.Getenv("GOMEMLIMIT"), os.Getenv("KARDINAL_MEMORY_LIMIT")); err != nil {
		logger.Warn().Err(err).Msg("Go memory limit not set")
	} else if limit > 0 {
		logger.Info().Int64("bytes", limit).Str("from", why).Msg("Go soft memory limit set")
	}

	tracingCfg.ServiceVersion = ControllerVersion
	tracingCfg.OnError = func(err error) { logger.Warn().Err(err).Msg("OpenTelemetry: span export failed") }
	shutdownTracing, err := tracing.Setup(context.Background(), tracingCfg)
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid tracing configuration")
	}
	if tracingCfg.Enabled {
		logger.Info().Str("endpoint", tracingCfg.Endpoint).Float64("samplingRatio", tracingCfg.SamplingRatio).
			Msg("OpenTelemetry tracing enabled")
	}

	allowlist, err := egress.ParseAllowlist(splitCSV(egressAllowlist))
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid --egress-allowlist")
	}
	egress.SetAllowlist(allowlist)
	if allowlist != nil {
		logger.Info().Str("egressAllowlist", allowlist.String()).
			Msg("egress allowlist set: NotificationHook, MetricCheck and Subscription requests reach only these destinations")
	}

	allowedRepos, err := scm.ParseRepositoryAllowlist(splitCSV(scmAllowedRepositories))
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid --scm-allowed-repositories")
	}
	if allowedRepos == nil {
		logger.Warn().Msg("--scm-allowed-repositories (Helm scm.allowedRepositories) is not set: any Pipeline " +
			"can have the controller's SCM token open PRs and delete kardinal/ branches in any repository " +
			"that token can write to; see docs/guides/security.md")
	} else {
		logger.Info().Strs("allowedRepositories", allowedRepos.Patterns()).
			Msg("the controller's SCM token is limited to the allowed repositories")
	}

	trustedProxies, err := accesslog.ParseCIDRs(splitCSV(accessLogTrustedProxies))
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid --access-log-trusted-proxies")
	}
	accessLog := accesslog.New(accesslog.Config{AllRequests: accessLogAll, SourceIP: accessLogSourceIP,
		TrustedProxies: trustedProxies}, logger.With().Str("component", "access").Logger())

	uiHosts, err := parseUIAllowedHosts(uiAllowedHosts)
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid --ui-allowed-hosts")
	}

	if watchNamespace != "" {
		logger.Info().Str("watchNamespace", watchNamespace).
			Msg("namespace-scoped mode: controller cache limited to single namespace")
	}
	if namespaceShard != "" {
		if err := shard.ValidateName(namespaceShard); err != nil {
			logger.Fatal().Err(err).Msg("invalid --namespace-shard")
		}
		if watchNamespace != "" {
			logger.Fatal().Msg("--namespace-shard and --watch-namespace cannot be combined: " +
				"a namespace-scoped controller already owns exactly one namespace")
		}
		logger.Info().Str("shard", namespaceShard).Msg("sharded: reconciling the namespaces of this shard only")
	}

	restConfig := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(restConfig, buildManagerOptions(managerConfig{
		restConfig:             restConfig,
		metricsBindAddress:     metricsBindAddress,
		healthProbeBindAddress: healthProbeBindAddress,
		leaderElect:            leaderElect,
		watchNamespace:         watchNamespace,
		namespaceShard:         namespaceShard,
	}))
	if err != nil {
		logger.Fatal().Err(err).Msg("unable to create manager")
	}
	// The shard gate must be in place before the reconcilers are set up:
	// each wraps itself in shard.Active().
	shardHome := os.Getenv("POD_NAMESPACE")
	if shardHome == "" {
		shardHome = "kardinal-system"
	}
	gateClient, gateReader, err := shardClients(mgr, namespaceShard)
	if err != nil {
		logger.Fatal().Err(err).Msg("unable to create the shard gate's clients")
	}
	gate := shard.New(shard.Options{Name: namespaceShard, Home: shardHome, Client: gateClient,
		Reader: gateReader, Recorder: mgr.GetEventRecorder("kardinal-shard"), Log: logger})
	if err := shard.Setup(mgr, gate); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up the shard gate")
	}
	graphIdentity.Writer = mgr.GetClient()
	graphIdentity.Reader = mgr.GetAPIReader()

	// SCM provider — scm.NewProvider dispatches on the --scm-provider flag.
	// When --scm-token-secret-name is set, a DynamicProvider is used so that
	// credential rotation (Secret update) reloads the provider without a restart.
	var scmProvider scm.SCMProvider
	if scmTokenSecretName != "" {
		// Resolve the namespace: flag > env > controller namespace.
		if scmTokenSecretNamespace == "" {
			scmTokenSecretNamespace = os.Getenv("POD_NAMESPACE")
		}
		if scmTokenSecretNamespace == "" {
			scmTokenSecretNamespace = "kardinal-system"
		}
		if scmTokenSecretKey == "" {
			scmTokenSecretKey = "token"
		}

		dynProvider, dynErr := scm.NewDynamicProvider(scmProviderType, githubToken, scmAPIURL, webhookSecret)
		if dynErr != nil {
			logger.Fatal().Err(dynErr).Msg("unable to create dynamic SCM provider")
		}
		scmProvider = dynProvider

		// Register the SecretWatcher as a manager.Runnable — starts after caches are synced.
		watcher := scm.NewSecretWatcher(
			mgr.GetClient(),
			dynProvider,
			scmTokenSecretName,
			scmTokenSecretNamespace,
			scmTokenSecretKey,
			logger,
		)
		if addErr := mgr.Add(watcher); addErr != nil {
			logger.Fatal().Err(addErr).Msg("unable to register SCM credential watcher")
		}
		logger.Info().
			Str("secret", scmTokenSecretNamespace+"/"+scmTokenSecretName).
			Str("key", scmTokenSecretKey).
			Msg("SCM credential watcher enabled — token will be reloaded on Secret change")
	} else {
		var provErr error
		scmProvider, provErr = scm.NewProvider(scmProviderType, githubToken, scmAPIURL, webhookSecret)
		if provErr != nil {
			logger.Fatal().Err(provErr).Msg("unable to create SCM provider")
		}
	}
	// Every SCM call the shared token makes is checked against
	// --scm-allowed-repositories, whichever code path makes it (#1332).
	if allowedRepos != nil {
		scmHost, hostErr := scm.WebHost(scmProviderType, scmAPIURL)
		if hostErr != nil {
			logger.Fatal().Err(hostErr).Msg("--scm-allowed-repositories needs the SCM host")
		}
		scmProvider = allowedRepos.Guard(scmProvider, scmHost)
	}
	gitClient := scm.NewGoGitClient()

	// ScmProviders and ClusterScmProviders: a Pipeline with
	// spec.git.providerRef opens its PRs with that provider's client, built
	// here from its Secret; a Pipeline without one keeps scmProvider.
	providers := &scm.Registry{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		// A provider's apiURL is written by a namespace user: its requests
		// go through the egress guard (no loopback, link-local or cloud
		// metadata addresses), and http:// only with the admin's opt-in.
		Transport: egress.NewTransport(http.ProxyFromEnvironment),
		AllowHTTP: scmProvidersAllowHTTP,
	}
	for _, cluster := range []bool{false, true} {
		if err := (&scmproviderrecon.Reconciler{Client: mgr.GetClient(), Registry: providers, Cluster: cluster}).SetupWithManager(mgr); err != nil {
			logger.Fatal().Err(err).Bool("cluster", cluster).Msg("unable to set up ScmProviderReconciler")
		}
	}

	// Reconcilers write events.k8s.io/v1 Events. The chart grants create and
	// patch on events.k8s.io events for this recorder.
	eventRecorder := mgr.GetEventRecorder("kardinal-controller")

	if err := retire.Validate(); err != nil {
		logger.Fatal().Err(err).Msg("invalid --graph-retire-*-after")
	}
	if retire == (bundlereconciler.RetirePolicy{}) {
		logger.Info().Msg("every --graph-retire-*-after is 0: finished Bundles keep their Graphs unless their Pipeline sets kardinal.io/graph-retire-after")
	}
	if err := (&bundlereconciler.Reconciler{
		Workers: *workers["bundle"],
		Client:  mgr.GetClient(),
		// Uncached: the maxConcurrentPromotions count must see the Promoting
		// patch of the previous reconcile (#1310).
		APIReader:        mgr.GetAPIReader(),
		Translator:       newTranslator(mgr, graphIdentity, splitCSV(policyNamespaces), providers, graphCompactAbove, logger),
		GraphChecker:     newGraphClient(mgr.GetConfig(), logger),
		Recorder:         eventRecorder,
		PolicyNamespaces: splitCSV(policyNamespaces),
		Retire:           retire,
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up BundleReconciler")
	}

	// Graph cleanup: prunes reader RoleBindings when a Graph is deleted, and
	// lets kardinal's Graphs in a terminating namespace go once kro can no
	// longer tear them down (its applier RoleBinding is gone).
	graphLister := newGraphClient(mgr.GetConfig(), logger)
	if err := (&graphcleanup.Reconciler{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Graphs:    graphLister,
		Identity:  graphIdentity,
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up GraphCleanupReconciler")
	}
	// The sweep lists RoleBindings cluster-wide, which namespace mode does not
	// grant; there the controller binds the reader role only in the watched
	// namespace, and the reconciler's prune covers it.
	// The sweep lists cluster-wide; in a sharded cluster the default shard runs it.
	if watchNamespace == "" && gate.OwnsClusterScoped() {
		if err := mgr.Add(&graphcleanup.Sweep{
			APIReader: mgr.GetAPIReader(),
			Graphs:    graphLister,
			Identity:  graphIdentity,
		}); err != nil {
			logger.Fatal().Err(err).Msg("unable to register the reader RoleBinding sweep")
		}
	}

	// AuditEvent retention: the leader deletes old records (they have no
	// owner, so nothing else does).
	if auditRetention {
		// A client of its own, uncached and slow (5 requests a second), so a
		// large backlog never takes API capacity from the reconcilers.
		retentionCfg := rest.CopyConfig(mgr.GetConfig())
		retentionCfg.QPS, retentionCfg.Burst = auditretention.QPS, auditretention.Burst
		retentionClient, err := sigs_client.New(retentionCfg, sigs_client.Options{Scheme: mgr.GetScheme(), Mapper: mgr.GetRESTMapper()})
		if err != nil {
			logger.Fatal().Err(err).Msg("unable to create the AuditEvent retention client")
		}
		if err := mgr.Add(&auditretention.Pruner{
			Client:         retentionClient,
			Namespace:      watchNamespace,
			MaxAge:         auditMaxAge,
			MaxPerPipeline: auditMaxPerPipeline,
			Interval:       auditRetentionInterval,
		}); err != nil {
			logger.Fatal().Err(err).Msg("unable to register AuditEvent retention")
		}
	} else {
		logger.Info().Msg("AuditEvent retention off (--audit-retention=false): every record is kept")
	}

	if err := (&pipelinereconciler.Reconciler{Client: mgr.GetClient(), AllowedRepositories: allowedRepos,
		CompactAbove: &graphCompactAbove, Workers: *workers["pipeline"]}).
		SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up PipelineReconciler")
	}

	pgReconciler, err := policygaterecon.NewReconciler(mgr.GetClient())
	if err != nil {
		logger.Fatal().Err(err).Msg("unable to create PolicyGateReconciler (CEL env init failed)")
	}
	pgReconciler.Recorder = eventRecorder
	// An org gate's instance reads metrics.* from its template's org policy
	// namespace, the same namespaces the translator takes org gates from.
	pgReconciler.PolicyNamespaces = splitCSV(policyNamespaces)
	pgReconciler.StatusHeartbeat = gateStatusHeartbeat
	pgReconciler.Workers = *workers["policygate"]
	pgReconciler.MaxOverride = time.Duration(gateOverrideMaxMinutes) * time.Minute
	pgReconciler.IdentityPolicy = &policygaterecon.IdentityPolicyCheck{Reader: mgr.GetAPIReader(), Name: overrideIdentityPolicy}
	if overrideIdentityPolicy == "" {
		logger.Warn().Msg("--override-identity-policy is not set: gate overrides are recorded with an unverified createdBy")
	}
	if err := pgReconciler.SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up PolicyGateReconciler")
	}

	if err := (&psreconciler.Reconciler{
		Workers:             *workers["promotionstep"],
		Client:              mgr.GetClient(),
		APIReader:           mgr.GetAPIReader(),
		SCM:                 scmProvider,
		AllowedRepositories: allowedRepos,
		Providers:           providers,
		GitClient:           gitClient,
		GatesStatusDisabled: !gatesCommitStatus,
		GatesStatusContext:  gatesStatusContext,
		HealthDetector:      newHealthDetector(mgr.GetConfig(), mgr.GetClient(), logger),
		RemoteClusters:      &healthpkg.RemoteClusters{},
		Recorder:            eventRecorder,
		SCMWaitTimeout:      scmWaitTimeout,
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up PromotionStepReconciler")
	}

	if _, err := hookrunrecon.ParsePodSecurityLevel(hookPodSecurityLevel); err != nil {
		logger.Fatal().Err(err).Msg("invalid --hook-pod-security-level")
	}
	hookControllerNS := os.Getenv("POD_NAMESPACE")
	if hookControllerNS == "" {
		hookControllerNS = "kardinal-system"
	}
	if err := (&hookrunrecon.Reconciler{
		Client:                 mgr.GetClient(),
		APIReader:              mgr.GetAPIReader(),
		AllowedServiceAccounts: splitCSV(hookServiceAccounts),
		GraphServiceAccount:    graphIdentity.ServiceAccountName,
		ControllerNamespace:    hookControllerNS,
		PodSecurityLevel:       hookPodSecurityLevel,
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up HookRunReconciler")
	}

	ivSCMHost, ivHostErr := scm.WebHost(scmProviderType, scmAPIURL)
	if ivHostErr != nil {
		logger.Warn().Err(ivHostErr).Msg("no SCM host: image policies with commits.requireSigned will fail")
	}
	if err := (&ivrecon.Reconciler{
		Client:          mgr.GetClient(),
		Registry:        &ivrecon.OCIRegistry{},
		SCM:             scmProvider,
		SCMHost:         ivSCMHost,
		InstanceSigners: splitCSV(scmInstanceSigners),
		PublicGoodRoot: ivrecon.PublicGoodRoot(func() (sigroot.TrustedMaterial, error) {
			// In memory: the controller's root file system is read-only.
			// Every TUF request is time-bounded and egress-guarded.
			opts := sigtuf.DefaultOptions().WithDisableLocalCache()
			opts.Fetcher = tuffetcher.NewDefaultFetcher().NewFetcherWithHTTPClient(&http.Client{
				Timeout:   ivrecon.PublicGoodFetchTimeout,
				Transport: egress.NewTransport(http.ProxyFromEnvironment),
			})
			return sigroot.FetchTrustedRootWithOptions(opts)
		}),
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up ImageVerificationReconciler")
	}

	if err := (&metriccheckrecon.Reconciler{
		Client:   mgr.GetClient(),
		Backends: metriccheckrecon.DefaultBackends(cloudWatchAmbient),
		Limiter:  metriccheckrecon.NewLimiter(metricGlobalSlots, metricNamespaceSlots),
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up MetricCheckReconciler")
	}

	if err := (&prstatusrecon.Reconciler{
		Workers:   *workers["prstatus"],
		Client:    mgr.GetClient(),
		SCM:       scmProvider,
		Providers: providers,
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up PRStatusReconciler")
	}

	if err := (&rbprecon.Reconciler{
		Client:   mgr.GetClient(),
		Recorder: eventRecorder,
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up RollbackPolicyReconciler")
	}

	// ScheduleClockReconciler: writes status.tick on interval, generating watch events
	// that trigger PolicyGate re-evaluation for schedule.* expressions.
	// One ScheduleClock per cluster (kardinal-system) is sufficient.
	if err := (&scheduleclockrecon.Reconciler{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up ScheduleClockReconciler")
	}

	// ChangeWindowReconciler: evaluates each ChangeWindow (blackout or recurring),
	// writes status.active/reason and requeues at the next boundary. The status write
	// re-evaluates the PolicyGates that reference the window.
	cwReconciler := &changewindowrecon.Reconciler{Client: mgr.GetClient()}
	// An Event on a ChangeWindow, a cluster-scoped kind, is written in the
	// default namespace, where namespace mode grants no Event writes. There the
	// Valid condition and the log report an invalid spec.
	if watchNamespace == "" {
		cwReconciler.Recorder = eventRecorder
	}
	if err := cwReconciler.SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up ChangeWindowReconciler")
	}

	// NotificationHookReconciler: watches Bundle, PolicyGate, and PromotionStep objects
	// and delivers outbound webhooks when promotion events occur.
	if err := (&nhookrecon.Reconciler{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up NotificationHookReconciler")
	}

	// SubscriptionReconciler: polls OCI registries, Git repositories and Helm
	// chart repositories on an interval (or at once on a kardinal.io/refresh
	// request from the webhook receiver) and creates Bundle CRDs when new
	// artifacts are detected. WatcherFn nil is subscriptionrecon.NewWatcher.
	if err := (&subscriptionrecon.Reconciler{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up SubscriptionReconciler")
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up health check")
	}
	// readyz gates on informer cache sync: returns 503 until all informers are synced.
	// Using healthz.Ping here would cause the pod to report as Ready before the cache is
	// populated, which leads to a race condition where Bundles are created but the reconciler
	// hasn't started yet (seen in old e2e runs: "Starting EventSource" at check time).
	// The cache-sync check ensures helm --wait only returns after reconcilers can process events.
	// See: https://github.com/pnz1990/kardinal-promoter/issues/1132
	cacheSyncChecker := healthz.Checker(func(req *http.Request) error {
		ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
		defer cancel()
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			return fmt.Errorf("informer cache not yet synced")
		}
		return nil
	})
	if err := mgr.AddReadyzCheck("readyz", cacheSyncChecker); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up ready check")
	}

	// Webhook server: SCM webhooks and the bundle API.
	webhookSrv := newWebhookServerWithConfig(scmProvider, mgr.GetClient(), logger, webhookSecret != "")
	if webhookSecret == "" {
		logger.Warn().Msg("SCM webhooks disabled: no --webhook-secret set, /webhook/scm rejects every event; merges are detected by PR status polling")
	}
	bundleAPIToken := bundleToken
	mux := http.NewServeMux()
	mux.Handle("/webhook/scm", tracing.Handler("webhook.scm", webhookSrv.Handler()))
	mux.HandleFunc("/webhook/scm/health", webhookSrv.HealthHandler())
	// Registry and SCM webhooks that make a Subscription poll at once. Each
	// Subscription opts in with spec.webhook and its own token.
	mux.HandleFunc(subscriptionWebhookPrefix, newSubscriptionWebhook(mgr.GetClient(), logger).Handler())
	mux.HandleFunc(openAPIPath, handleOpenAPI)
	// Each ScmProvider and ClusterScmProvider has its own endpoint, checked
	// with its own webhook secret (docs/scm-providers.md).
	mux.HandleFunc("POST /webhook/scm/namespaces/{namespace}/{name}", webhookSrv.ProviderHandler(providers))
	mux.HandleFunc("POST /webhook/scm/cluster/{name}", webhookSrv.ProviderHandler(providers))
	// Bundle API endpoint — only mounted if a token is configured.
	if bundleAPIToken != "" {
		// Default to the watched namespace; in namespace-scoped mode it is
		// also the only namespace Bundles may be created in.
		bundleNS := "default"
		if watchNamespace != "" {
			bundleNS = watchNamespace
		}
		bundleAPI := newBundleAPIServerWithLogger(mgr.GetClient(), bundleAPIToken, bundleNS, logger)
		bundleAPI.onlyNamespace = watchNamespace
		bundleAPI.reader = mgr.GetAPIReader()
		mux.Handle("/api/v1/bundles", accessLog.Middleware("bundle-api", tracing.Handler("bundleapi.create", bundleAPI.Handler())))
		logger.Info().Msg("bundle API endpoint enabled at /api/v1/bundles")
	}
	// The webhook and UI servers are manager Runnables: they start after the
	// caches sync, a bind failure stops the controller, and shutdown drains
	// in-flight requests.
	webhookServer, err := newHTTPServer("webhook", webhookBindAddress, mux, tlsCertFile, tlsKeyFile, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("unable to configure webhook server")
	}
	// Reports access log lines dropped over their per-second budget.
	if err := mgr.Add(accessLog); err != nil {
		logger.Fatal().Err(err).Msg("unable to add the access log reporter")
	}
	if err := mgr.Add(webhookServer); err != nil {
		logger.Fatal().Err(err).Msg("unable to add webhook server")
	}

	// UI API authentication. TokenReview mode fails closed: the controller does
	// not start when the review clients cannot be built, instead of serving an
	// open UI.
	uiAuth, err := buildUIAuth(mgr.GetConfig(), uiAuthToken, uiTokenReviewAuth, watchNamespace)
	if err != nil {
		logger.Fatal().Err(err).Msg("UI API TokenReview: unable to create the review clients")
	}
	switch {
	case uiAuth.staticToken != "":
		// O4 (spec issue-975): the static token takes precedence over TokenReview.
		logger.Info().Msg("UI API authentication enabled (--ui-auth-token set)")
	case uiAuth.tokens != nil:
		logger.Info().Msg("UI API TokenReview authentication enabled; every read and write is authorized with a SubjectAccessReview for the caller")
	default:
		logger.Warn().Msg("UI API authentication is off: /api/ answers only loopback clients (kubectl port-forward) and refuses the rest with 403. " +
			"Behind a service-mesh sidecar, loopback means any client in the mesh, so set an auth mode: " +
			"Helm ui.auth.tokenReview=true or ui.auth.tokenSecretRef.name (--ui-tokenreview-auth, --ui-auth-token)")
	}

	// Embedded UI server: the React app at /ui/ and its API.
	distFS, err := fs.Sub(web.Assets, "dist")
	if err != nil {
		logger.Error().Err(err).Msg("failed to create UI sub-filesystem")
		distFS = nil
	}
	uiServer, err := newHTTPServer("ui", uiListenAddress,
		accessLog.Middleware("ui", newUIHandler(mgr.GetClient(), distFS, uiAuth, corsAllowedOrigins, uiHosts, logger)),
		tlsCertFile, tlsKeyFile, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("unable to configure UI server")
	}
	if err := mgr.Add(uiServer); err != nil {
		logger.Fatal().Err(err).Msg("unable to add UI server")
	}

	logger.Info().Msg("starting kardinal-controller")

	// SCM token scope preflight check (non-fatal; see checkSCMTokenAtStartup).
	// It runs whenever a token is set, including chart installs that also set
	// --scm-token-secret-name: GITHUB_TOKEN comes from that same Secret, so it
	// is the token the watcher starts with. It runs in the background so an
	// unreachable SCM API does not delay startup.
	go checkSCMTokenAtStartup(context.Background(), logger, scmProviderType, githubToken, scmAPIURL)

	// Register a Runnable that creates/updates the kardinal-version ConfigMap
	// after the controller starts. `kardinal version` reads this ConfigMap.
	controllerNS := os.Getenv("POD_NAMESPACE")
	if controllerNS == "" {
		controllerNS = "kardinal-system"
	}
	if err := mgr.Add(&versionRunnable{
		client:    mgr.GetClient(),
		namespace: controllerNS,
		version:   ControllerVersion,
		log:       logger,
	}); err != nil {
		logger.Warn().Err(err).Msg("failed to register version ConfigMap runnable")
	}

	// Nothing may reconcile after Start returns: a leader has released its
	// Lease by then (LeaderElectionReleaseOnCancel), and a standby may
	// already lead. Flushing buffered spans is not reconciling: it runs
	// after every reconciler and HTTP server has drained, so their last
	// spans are exported too.
	signals := ctrl.SetupSignalHandler()
	if err := runThenFlush(func() error { return mgr.Start(signals) }, shutdownTracing, logger); err != nil {
		logger.Fatal().Err(err).Msg("problem running manager")
	}
}

// ensureVersionConfigMap creates or updates the kardinal-version ConfigMap in the
// controller's namespace. This ConfigMap is read by `kardinal version` to display
// the controller and graph engine versions.
//
// This function is called as a controller-runtime Runnable (AddRunnable) so it
// runs after the manager's caches are synced but before the main reconciliation loop.
// It is idempotent: safe to call multiple times.
func ensureVersionConfigMap(ctx context.Context, c sigs_client.Client, namespace, controllerVer string, log zerolog.Logger) {
	key := types.NamespacedName{Name: "kardinal-version", Namespace: namespace}
	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, key, cm)
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kardinal-version",
				Namespace: namespace,
			},
			Data: map[string]string{
				"version": controllerVer,
			},
		}
		if createErr := c.Create(ctx, cm); createErr != nil && !apierrors.IsAlreadyExists(createErr) {
			log.Warn().Err(createErr).Msg("failed to create kardinal-version ConfigMap")
		} else {
			log.Info().Str("version", controllerVer).Msg("created kardinal-version ConfigMap")
		}
		return
	}
	if err != nil {
		log.Warn().Err(err).Msg("failed to check kardinal-version ConfigMap")
		return
	}
	// Update if version changed.
	if cm.Data["version"] != controllerVer {
		patch := sigs_client.MergeFrom(cm.DeepCopy())
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}
		cm.Data["version"] = controllerVer
		if patchErr := c.Patch(ctx, cm, patch); patchErr != nil {
			log.Warn().Err(patchErr).Msg("failed to update kardinal-version ConfigMap")
		} else {
			log.Info().Str("version", controllerVer).Msg("updated kardinal-version ConfigMap")
		}
	}
}

// versionRunnable is a manager.Runnable that writes the controller version to
// a ConfigMap on startup. It implements the controller-runtime Runnable interface.
type versionRunnable struct {
	client    sigs_client.Client
	namespace string
	version   string
	log       zerolog.Logger
}

// Start implements manager.Runnable.
func (v *versionRunnable) Start(ctx context.Context) error {
	ensureVersionConfigMap(ctx, v.client, v.namespace, v.version, v.log)
	return nil
}

// newHealthDetector constructs an AutoDetector for health checking.
// It creates a dynamic client from the given REST config.
func newHealthDetector(cfg *rest.Config, k8s sigs_client.Client, log zerolog.Logger) *healthpkg.AutoDetector {
	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("unable to create dynamic client for health detection")
	}
	return healthpkg.NewAutoDetector(k8s, dynClient)
}

// newTranslator constructs the Translator wired with a GraphClient, Builder,
// and the Graph identity provisioner.
func newTranslator(mgr ctrl.Manager, identity *graphpkg.IdentityProvisioner,
	policyNS []string, providers *scm.Registry, compactAbove int, log zerolog.Logger) *translator.Translator {
	dynClient, err := dynamic.NewForConfig(mgr.GetConfig())
	if err != nil {
		log.Fatal().Err(err).Msg("unable to create dynamic client for graph")
	}
	graphClient := graphpkg.NewGraphClient(dynClient, log)
	builder := graphpkg.NewBuilder()
	builder.ServiceAccountName = identity.ServiceAccountName
	builder.CompactAbove = compactAbove
	return translator.New(graphClient, builder, mgr.GetClient(), policyNS, log).
		WithIdentity(identity).
		WithRESTMapper(mgr.GetRESTMapper()).
		WithProviders(providers)
}

// newGraphClient constructs a GraphClient for use as a GraphChecker in the Bundle reconciler.
// A separate dynamic client is created so the Translator and BundleReconciler each have
// their own client handle (avoids sharing state across goroutines).
func newGraphClient(cfg *rest.Config, log zerolog.Logger) *graphpkg.GraphClient {
	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("unable to create dynamic client for graph checker")
	}
	return graphpkg.NewGraphClient(dynClient, log)
}

// splitCSV splits a comma-separated string into a trimmed slice.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := strings.TrimSpace(s[start:i])
			if part != "" {
				result = append(result, part)
			}
			start = i + 1
		}
	}
	return result
}

// ptr returns a pointer to v — used for optional ctrl.Options fields. (#574)
func ptr[T any](v T) *T { return &v }

// applyCORSMiddleware wraps handler with CORS enforcement for /api/v1/ui/* routes.
//
// Policy:
//   - allowedOriginsCSV == "":  same-origin only. Cross-origin requests receive 403.
//   - allowedOriginsCSV == "*": all origins allowed (development / opt-out).
//   - otherwise: comma-separated list. Only listed origins receive CORS headers.
//
// A request is same-origin only when its Host is in hosts (loopback plus
// --ui-allowed-hosts) and its Origin names that Host: under DNS rebinding a
// hostile page sends matching Origin and Host headers for its own name. While
// UI auth is off (authEnabled false), every /api/ request with a Host outside
// hosts is rejected, with or without an Origin and reads included: a rebound
// page's same-origin GET or form post carries no Origin, and no credential
// stands in its way. With auth on, the credential protects such requests.
//
// CORS headers are only written for /api/v1/ui/* paths. Static /ui/* assets
// (the same for everyone) and webhook routes are not affected.
func applyCORSMiddleware(next http.Handler, allowedOriginsCSV string, hosts uiHostAllowlist, authEnabled bool, log zerolog.Logger) http.Handler {
	// Parse allow-list once at startup.
	allowAll := allowedOriginsCSV == "*"
	allowedSet := make(map[string]struct{})
	if !allowAll {
		for _, o := range strings.Split(allowedOriginsCSV, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				allowedSet[o] = struct{}{}
			}
		}
	}

	switch {
	case allowAll:
		log.Warn().Msg("CORS: all origins allowed (--cors-allowed-origins=*). Use an explicit list in production.")
	case len(allowedSet) > 0:
		origins := make([]string, 0, len(allowedSet))
		for o := range allowedSet {
			origins = append(origins, o)
		}
		log.Info().Strs("origins", origins).Msg("CORS: allow-list configured for /api/v1/ui/*")
	default:
		log.Info().Msg("CORS: same-origin only for /api/v1/ui/* (no --cors-allowed-origins set)")
	}
	log.Info().Strs("hosts", hosts.names()).
		Msg("UI API: Host names accepted as same-origin, on top of localhost, 127.0.0.1 and ::1 (--ui-allowed-hosts)")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostAllowed := hosts.allows(r.Host)
		if !authEnabled && !hostAllowed && strings.HasPrefix(r.URL.Path, "/api/") {
			// No credential stands between a rebound page and this request.
			http.Error(w, uiHostNotAllowedMsg, http.StatusForbidden)
			return
		}

		// Only apply CORS logic to UI API routes.
		if !strings.HasPrefix(r.URL.Path, "/api/v1/ui/") {
			next.ServeHTTP(w, r)
			return
		}

		origin := r.Header.Get("Origin")
		if origin == "" {
			// Not a cross-origin browser request — pass through.
			next.ServeHTTP(w, r)
			return
		}

		// Same-origin request: browsers send Origin on every POST, including
		// the embedded UI's own. Origin host:port equal to the Host header is the
		// same origin (a single port cannot serve two schemes), but only for a
		// Host this server knows as its own name (DNS rebinding).
		if hostAllowed && isSameOrigin(origin, r.Host) {
			next.ServeHTTP(w, r)
			return
		}

		// Cross-origin request: check allow-list.
		allowed := allowAll
		if !allowed {
			_, allowed = allowedSet[origin]
		}

		if !allowed {
			// Reject: cross-origin request from unlisted origin.
			http.Error(w, "CORS: origin not allowed", http.StatusForbidden)
			return
		}

		// Write CORS headers for allowed origins.
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Vary", "Origin")

		// Handle preflight requests.
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isSameOrigin reports whether the Origin header names the host:port the
// request was sent to.
func isSameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return strings.EqualFold(u.Host, host)
}
