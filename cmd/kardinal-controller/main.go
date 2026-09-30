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
	admissionpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/admission"
	graphpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	healthpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	bundlereconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	changewindowrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
	metriccheckrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/metriccheck"
	nhookrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/notificationhook"
	pipelinereconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
	policygaterecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
	psreconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	prstatusrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
	rbprecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/rollbackpolicy"
	scheduleclockrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/scheduleclock"
	subscriptionrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/subscription"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
	"github.com/kardinal-promoter/kardinal-promoter/web"

	// Import built-in steps to register them via init().
	_ "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"

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
	)

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
		"SCM token for API operations (GitHub PAT or GitLab private token).")
	flag.StringVar(&webhookSecret, "webhook-secret", os.Getenv("KARDINAL_WEBHOOK_SECRET"),
		"Secret for validating incoming SCM webhooks (HMAC for GitHub, plaintext token for GitLab).")
	flag.StringVar(&scmProviderType, "scm-provider", os.Getenv("KARDINAL_SCM_PROVIDER"),
		"SCM provider type: \"github\" (default) or \"gitlab\".")
	flag.StringVar(&scmAPIURL, "scm-api-url", os.Getenv("KARDINAL_SCM_API_URL"),
		"SCM API base URL override (e.g. for GitHub Enterprise or self-managed GitLab).")

	var bundleToken string
	flag.StringVar(&bundleToken, "bundle-api-token", os.Getenv("KARDINAL_BUNDLE_TOKEN"),
		"Bearer token for authenticating POST /api/v1/bundles requests.")

	var uiAuthToken string
	flag.StringVar(&uiAuthToken, "ui-auth-token", os.Getenv("KARDINAL_UI_TOKEN"),
		"Bearer token for authenticating /api/v1/ui/* requests. "+
			"When set, all UI API routes require 'Authorization: Bearer <token>'. "+
			"When empty (default), the UI API is open (no authentication). "+
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

	var shard string
	flag.StringVar(&shard, "shard", os.Getenv("KARDINAL_SHARD"),
		"Shard name for distributed mode. When set, this controller only processes PromotionSteps "+
			"with a matching kardinal.io/shard label. Leave empty for standalone (single-controller) mode.")

	// SCM credential rotation — watch a Kubernetes Secret and reload the SCM
	// provider on change without restarting the controller. When
	// --scm-token-secret-name is set, the --github-token flag is used only as
	// the initial value (bootstrapping) and the Secret becomes the authoritative
	// source thereafter.
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

	// --pipeline-admission-webhook enables the ValidatingAdmissionWebhook handler for
	// Pipeline CRDs. When true, the handler is mounted at
	// POST /webhook/validate/pipeline on the webhook server (--webhook-bind-address).
	// Operators must separately install a ValidatingWebhookConfiguration pointing at
	// this path; the controller does not auto-create it.
	// Design ref: docs/design/15-production-readiness.md §Lens 4
	var pipelineAdmissionWebhook bool
	flag.BoolVar(&pipelineAdmissionWebhook, "pipeline-admission-webhook",
		os.Getenv("KARDINAL_PIPELINE_ADMISSION_WEBHOOK") == "true",
		"Enable the ValidatingAdmissionWebhook handler for Pipeline cycle detection "+
			"at POST /webhook/validate/pipeline. Requires a ValidatingWebhookConfiguration "+
			"to be installed separately. Also readable from "+
			"KARDINAL_PIPELINE_ADMISSION_WEBHOOK=true environment variable.")

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

	// Graph identity: kro applies each Graph as this ServiceAccount in the
	// Pipeline's namespace. The controller creates it and binds it to the two
	// ClusterRoles the chart ships (templates/graph-rbac.yaml).
	graphIdentity := graphpkg.IdentityProvisioner{}
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

	// controller-runtime uses its own flag set; parse standard flags here
	opts := czap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	graphIdentity.ReaderNamespaces = splitCSV(graphReaderNamespaces)

	// Configure zerolog level
	level, err := zerolog.ParseLevel(zerologLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	// Reconcilers log through zerolog.Ctx(ctx). controller-runtime does not put a
	// zerolog logger in the reconcile context, so without this default every
	// reconciler line, errors included, goes to a disabled logger.
	zerolog.DefaultContextLogger = &logger

	if shard != "" {
		logger.Info().Str("shard", shard).Msg("controller started in distributed mode")
	} else {
		logger.Info().Msg("controller started in standalone mode")
	}

	ctrl.SetLogger(czap.New(czap.UseFlagOptions(&opts)))

	uiHosts, err := parseUIAllowedHosts(uiAllowedHosts)
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid --ui-allowed-hosts")
	}

	if watchNamespace != "" {
		logger.Info().Str("watchNamespace", watchNamespace).
			Msg("namespace-scoped mode: controller cache limited to single namespace")
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), buildManagerOptions(managerConfig{
		metricsBindAddress:     metricsBindAddress,
		healthProbeBindAddress: healthProbeBindAddress,
		leaderElect:            leaderElect,
		watchNamespace:         watchNamespace,
	}))
	if err != nil {
		logger.Fatal().Err(err).Msg("unable to create manager")
	}

	// SCM provider — dispatches to GitHub or GitLab based on --scm-provider flag.
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
	gitClient := scm.NewGoGitClient()

	if err := (&bundlereconciler.Reconciler{
		Client:       mgr.GetClient(),
		Translator:   newTranslator(mgr, graphIdentity, splitCSV(policyNamespaces), logger),
		GraphChecker: newGraphClient(mgr.GetConfig(), logger),
		Recorder:     mgr.GetEventRecorderFor("kardinal-controller"), //nolint:staticcheck
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up BundleReconciler")
	}

	if err := (&pipelinereconciler.Reconciler{Client: mgr.GetClient()}).
		SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up PipelineReconciler")
	}

	pgReconciler, err := policygaterecon.NewReconciler(mgr.GetClient())
	if err != nil {
		logger.Fatal().Err(err).Msg("unable to create PolicyGateReconciler (CEL env init failed)")
	}
	pgReconciler.Recorder = mgr.GetEventRecorderFor("kardinal-controller") //nolint:staticcheck
	if err := pgReconciler.SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up PolicyGateReconciler")
	}

	if err := (&psreconciler.Reconciler{
		Client:         mgr.GetClient(),
		SCM:            scmProvider,
		GitClient:      gitClient,
		HealthDetector: newHealthDetector(mgr.GetConfig(), mgr.GetClient(), logger),
		Shard:          shard,
		Recorder:       mgr.GetEventRecorderFor("kardinal-controller"), //nolint:staticcheck
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up PromotionStepReconciler")
	}

	if err := (&metriccheckrecon.Reconciler{
		Client:   mgr.GetClient(),
		Provider: metriccheckrecon.NewPrometheusProvider(),
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up MetricCheckReconciler")
	}

	if err := (&prstatusrecon.Reconciler{
		Client: mgr.GetClient(),
		SCM:    scmProvider,
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up PRStatusReconciler")
	}

	if err := (&rbprecon.Reconciler{
		Client: mgr.GetClient(),
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
	if err := (&changewindowrecon.Reconciler{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up ChangeWindowReconciler")
	}

	// NotificationHookReconciler: watches Bundle, PolicyGate, and PromotionStep objects
	// and delivers outbound webhooks when promotion events occur.
	if err := (&nhookrecon.Reconciler{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up NotificationHookReconciler")
	}

	// SubscriptionReconciler: polls OCI registries and Git repositories on an interval
	// and creates Bundle CRDs when new artifacts are detected.
	if err := (&subscriptionrecon.Reconciler{
		Client: mgr.GetClient(),
		WatcherFn: func(sub *kardinalv1alpha1.Subscription) (source.Watcher, error) {
			switch sub.Spec.Type {
			case kardinalv1alpha1.SubscriptionTypeImage:
				if sub.Spec.Image == nil {
					return nil, fmt.Errorf("image subscription missing spec.image")
				}
				return source.NewOCIWatcher(sub.Spec.Image.Registry, sub.Spec.Image.TagFilter), nil
			case kardinalv1alpha1.SubscriptionTypeGit:
				if sub.Spec.Git == nil {
					return nil, fmt.Errorf("git subscription missing spec.git")
				}
				return source.NewGitWatcher(sub.Spec.Git.RepoURL, sub.Spec.Git.Branch, sub.Spec.Git.PathGlob), nil
			default:
				return nil, fmt.Errorf("unknown subscription type %q", sub.Spec.Type)
			}
		},
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up SubscriptionReconciler")
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up health check")
	}
	// readyz gates on informer cache sync: returns 503 until all informers are synced.
	// Using healthz.Ping here would cause the pod to report as Ready before the cache is
	// populated, which leads to a race condition where Bundles are created but the reconciler
	// hasn't started yet (seen in PDCA S1 failures: "Starting EventSource" at check time).
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

	// Webhook server: SCM webhooks, bundle API, Pipeline admission.
	webhookSrv := newWebhookServerWithConfig(scmProvider, mgr.GetClient(), logger, webhookSecret != "")
	if webhookSecret == "" {
		logger.Warn().Msg("SCM webhooks disabled: no --webhook-secret set, /webhook/scm rejects every event; merges are detected by PR status polling")
	}
	bundleAPIToken := bundleToken
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/scm", webhookSrv.Handler())
	mux.HandleFunc("/webhook/scm/health", webhookSrv.HealthHandler())
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
		mux.HandleFunc("/api/v1/bundles", bundleAPI.Handler())
		logger.Info().Msg("bundle API endpoint enabled at /api/v1/bundles")
	}
	// Pipeline admission webhook — only mounted when explicitly enabled.
	// Requires a ValidatingWebhookConfiguration installed separately by the operator.
	// Design ref: docs/design/15-production-readiness.md §Lens 4
	if pipelineAdmissionWebhook {
		mux.HandleFunc("/webhook/validate/pipeline", admissionpkg.PipelineWebhookHandler(logger))
		logger.Info().Msg("pipeline admission webhook enabled at /webhook/validate/pipeline")
	}
	// The webhook and UI servers are manager Runnables: they start after the
	// caches sync, a bind failure stops the controller, and shutdown drains
	// in-flight requests.
	webhookServer, err := newHTTPServer("webhook", webhookBindAddress, mux, tlsCertFile, tlsKeyFile, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("unable to configure webhook server")
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
		newUIHandler(mgr.GetClient(), distFS, uiAuth, corsAllowedOrigins, uiHosts, logger),
		tlsCertFile, tlsKeyFile, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("unable to configure UI server")
	}
	if err := mgr.Add(uiServer); err != nil {
		logger.Fatal().Err(err).Msg("unable to add UI server")
	}

	logger.Info().Msg("starting kardinal-controller")

	// SCM token scope preflight check — validate that the configured token has
	// the scopes required for kardinal-promoter to open and manage pull requests.
	// This is a non-fatal startup check: warnings are logged but do not prevent
	// the controller from starting. A misconfigured token will surface as a 403
	// during the first open-pr step — surfacing it here means teams discover the
	// problem in minutes rather than hours.
	//
	// The check is skipped when:
	//   - the token is managed by a DynamicProvider (the initial token from --github-token
	//     may be empty; the actual token is loaded from the Secret by the watcher)
	//   - the provider type is not github/gitlab/forgejo (unsupported)
	//   - the call returns a transient network error (logged at debug level; non-fatal)
	if scmTokenSecretName == "" && githubToken != "" {
		scopeCtx, scopeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer scopeCancel()

		var scopeWarnings []scm.TokenScopeWarning
		var scopeErr error

		switch scmProviderType {
		case "", "github":
			scopeWarnings, scopeErr = scm.ValidateGitHubTokenScopes(scopeCtx, githubToken, scmAPIURL)
		case "gitlab":
			scopeWarnings, scopeErr = scm.ValidateGitLabTokenScopes(scopeCtx, githubToken, scmAPIURL)
		case "forgejo", "gitea":
			scopeWarnings, scopeErr = scm.ValidateForgejoTokenScopes(scopeCtx, githubToken, scmAPIURL)
		}

		if scopeErr != nil {
			logger.Debug().Err(scopeErr).
				Str("provider", scmProviderType).
				Msg("SCM token scope check skipped (network error — non-fatal)")
		}
		for _, w := range scopeWarnings {
			logger.Warn().
				Str("provider", scmProviderType).
				Str("missing_scope", w.MissingScope).
				Str("consequence", w.Consequence).
				Msg("SCM TOKEN SCOPE WARNING — promotion steps may fail when this scope is required")
		}
	}

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

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
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
func newTranslator(mgr ctrl.Manager, identity graphpkg.IdentityProvisioner,
	policyNS []string, log zerolog.Logger) *translator.Translator {
	dynClient, err := dynamic.NewForConfig(mgr.GetConfig())
	if err != nil {
		log.Fatal().Err(err).Msg("unable to create dynamic client for graph")
	}
	graphClient := graphpkg.NewGraphClient(dynClient, log)
	builder := graphpkg.NewBuilder()
	builder.ServiceAccountName = identity.ServiceAccountName
	identity.Writer = mgr.GetClient()
	identity.Reader = mgr.GetAPIReader()
	return translator.New(graphClient, builder, mgr.GetClient(), policyNS, log).
		WithIdentity(&identity).
		WithRESTMapper(mgr.GetRESTMapper())
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
