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
	"crypto/subtle"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
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
	"sigs.k8s.io/controller-runtime/pkg/cache"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	czap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	admissionpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/admission"
	graphpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	healthpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	bundlereconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
	"github.com/kardinal-promoter/kardinal-promoter/web"

	// Import built-in steps to register them via init().
	_ "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
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

	// controller-runtime uses its own flag set; parse standard flags here
	opts := czap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	// Configure zerolog level
	level, err := zerolog.ParseLevel(zerologLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()

	if shard != "" {
		logger.Info().Str("shard", shard).Msg("controller started in distributed mode")
	} else {
		logger.Info().Msg("controller started in standalone mode")
	}

	ctrl.SetLogger(czap.New(czap.UseFlagOptions(&opts)))

	// Build cache options: when watchNamespace is set, limit the informer cache to
	// that namespace only. This is the mechanism for namespace-scoped install mode.
	// In namespace-scoped mode the Helm chart renders a Role/RoleBinding instead of
	// a ClusterRole/ClusterRoleBinding. (docs/design/15-production-readiness.md §Lens 6)
	cacheOpts := cache.Options{}
	if watchNamespace != "" {
		cacheOpts.DefaultNamespaces = map[string]cache.Config{watchNamespace: {}}
		logger.Info().Str("watchNamespace", watchNamespace).
			Msg("namespace-scoped mode: controller cache limited to single namespace")
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsBindAddress,
		},
		HealthProbeBindAddress: healthProbeBindAddress,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "kardinal-promoter-leader",
		// GracefulShutdownTimeout allows in-flight reconcile loops to complete
		// before the controller exits. Set to 30s — half the pod's
		// terminationGracePeriodSeconds (60s) to leave room for cleanup. (#574)
		GracefulShutdownTimeout: ptr(30 * time.Second),
		// RecoverPanic is intentionally NOT set here. controller-runtime v0.23+ defaults
		// RecoverPanic to true: a panic in any reconciler's Reconcile() method is caught
		// by the framework, increments the ReconcilePanics metric, and returns a wrapped
		// error for exponential backoff. DO NOT set RecoverPanic to false — that would
		// revert to crash-loop-on-panic behaviour. (spec #920, docs/design/15-production-readiness.md)
		Cache: cacheOpts,
	})
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
		Translator:   newTranslator(mgr.GetConfig(), mgr.GetClient(), splitCSV(policyNamespaces), logger),
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
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up ready check")
	}

	// Start webhook server in a goroutine.
	go func() {
		webhookSrv := newWebhookServerWithConfig(scmProvider, mgr.GetClient(), logger, webhookSecret != "")
		bundleAPIToken := bundleToken
		mux := http.NewServeMux()
		mux.HandleFunc("/webhook/scm", webhookSrv.Handler())
		mux.HandleFunc("/webhook/scm/health", webhookSrv.HealthHandler())
		// Bundle API endpoint — only mounted if a token is configured.
		if bundleAPIToken != "" {
			bundleAPI := newBundleAPIServerWithLogger(mgr.GetClient(), bundleAPIToken, "default", logger)
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
		logger.Info().Str("addr", webhookBindAddress).Msg("starting webhook server")
		if err := listenAndServeWithTLS(webhookBindAddress, mux, tlsCertFile, tlsKeyFile, logger); err != nil {
			logger.Error().Err(err).Msg("webhook server error")
		}
	}()

	// Start embedded UI server in a goroutine.
	go func() {
		uiMux := http.NewServeMux()
		// Register read-only UI API routes.
		uiAPI := newUIAPIServer(mgr.GetClient(), logger)
		uiAPI.RegisterRoutes(uiMux)
		// Serve the embedded React app at /ui/.
		distFS, err := fs.Sub(web.Assets, "dist")
		if err != nil {
			logger.Error().Err(err).Msg("failed to create UI sub-filesystem")
		} else {
			uiMux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.FS(distFS))))
		}

		// Apply Bearer token authentication to all /api/v1/ui/* routes when
		// --ui-auth-token is set. Static /ui/* assets bypass auth (no sensitive data).
		var handler http.Handler = uiMux
		switch {
		case uiAuthToken != "":
			// O4 (spec issue-975): Static token takes precedence over TokenReview.
			logger.Info().Msg("UI API authentication enabled (--ui-auth-token set)")
			tokenBytes := []byte(uiAuthToken)
			handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Only guard /api/v1/ui/* — static assets at /ui/* are public.
				if strings.HasPrefix(r.URL.Path, "/api/v1/ui/") {
					authHeader := r.Header.Get("Authorization")
					provided := strings.TrimPrefix(authHeader, "Bearer ")
					// Use constant-time comparison to prevent timing attacks.
					if !strings.HasPrefix(authHeader, "Bearer ") ||
						subtle.ConstantTimeCompare([]byte(provided), tokenBytes) != 1 {
						w.Header().Set("Www-Authenticate", `Bearer realm="kardinal-ui"`)
						http.Error(w, "unauthorized", http.StatusUnauthorized)
						return
					}
				}
				uiMux.ServeHTTP(w, r)
			})
		case uiTokenReviewAuth:
			// O1–O3, O6–O8 (spec issue-975): Kubernetes TokenReview auth mode.
			// Only activated when --ui-auth-token is not set (O4).
			logger.Info().Msg("UI API TokenReview authentication enabled")
			reviewer, reviewerErr := uiauth.NewKubeTokenReviewer(mgr.GetConfig())
			if reviewerErr != nil {
				// Non-fatal: fall through to open mode with a warning. The controller
				// must still start — TokenReview unavailability should not block the
				// controller itself from serving other APIs.
				logger.Warn().Err(reviewerErr).
					Msg("UI API TokenReview: failed to create reviewer — UI API will be open (no auth)")
			} else {
				handler = uiauth.Middleware(uiMux, reviewer)
			}
		default:
			logger.Warn().Msg("UI API authentication disabled — set --ui-auth-token or --ui-tokenreview-auth to require authentication")
		}

		// Apply CORS lockdown to /api/v1/ui/* routes.
		// Default (empty corsAllowedOrigins): same-origin only — cross-origin requests rejected.
		// Explicit list: only listed origins are allowed.
		// Wildcard "*": all origins allowed (development / opt-out).
		handler = applyCORSMiddleware(handler, corsAllowedOrigins, logger)

		logger.Info().Str("addr", uiListenAddress).Msg("starting UI server")
		if err := listenAndServeWithTLS(uiListenAddress, handler, tlsCertFile, tlsKeyFile, logger); err != nil {
			logger.Error().Err(err).Msg("UI server error")
		}
	}()

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

// newTranslator constructs the Translator wired with a GraphClient and Builder.
func newTranslator(cfg *rest.Config, k8s sigs_client.Reader,
	policyNS []string, log zerolog.Logger) *translator.Translator {
	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("unable to create dynamic client for graph")
	}
	graphClient := graphpkg.NewGraphClient(dynClient, log)
	builder := graphpkg.NewBuilder()
	return translator.New(graphClient, builder, k8s, policyNS, log)
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
// CORS headers are only written for /api/v1/ui/* paths. Static /ui/* assets and
// webhook routes are not affected.
func applyCORSMiddleware(next http.Handler, allowedOriginsCSV string, log zerolog.Logger) http.Handler {
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

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only apply CORS logic to UI API routes.
		if !strings.HasPrefix(r.URL.Path, "/api/v1/ui/") {
			next.ServeHTTP(w, r)
			return
		}

		origin := r.Header.Get("Origin")
		if origin == "" {
			// Same-origin request (no Origin header) — pass through unconditionally.
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

// listenAndServeWithTLS starts an HTTP or HTTPS server depending on whether
// both certFile and keyFile are non-empty.
//   - Both set: use http.ListenAndServeTLS (HTTPS).
//   - Neither set: use http.ListenAndServe (plain HTTP — backwards compatible).
//   - Exactly one set: log a warning and fall back to plain HTTP.
func listenAndServeWithTLS(addr string, handler http.Handler, certFile, keyFile string, log zerolog.Logger) error {
	tlsEnabled := certFile != "" && keyFile != ""
	partialTLS := (certFile == "") != (keyFile == "") // exactly one is set

	if partialTLS {
		log.Warn().
			Str("tls-cert-file", certFile).
			Str("tls-key-file", keyFile).
			Msg("TLS: both --tls-cert-file and --tls-key-file must be set; falling back to plain HTTP")
	}

	if tlsEnabled {
		log.Info().Str("addr", addr).Msg("TLS enabled for server")
		return http.ListenAndServeTLS(addr, certFile, keyFile, handler)
	}
	return http.ListenAndServe(addr, handler)
}
