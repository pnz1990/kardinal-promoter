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
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	graphpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	healthpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	bundlereconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	metriccheckrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/metriccheck"
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

	var uiListenAddress string
	flag.StringVar(&uiListenAddress, "ui-listen-address", ":8082",
		"The address the embedded kardinal-ui HTTP server binds to.")

	var shard string
	flag.StringVar(&shard, "shard", os.Getenv("KARDINAL_SHARD"),
		"Shard name for distributed mode. When set, this controller only processes PromotionSteps "+
			"with a matching kardinal.io/shard label. Leave empty for standalone (single-controller) mode.")

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
	})
	if err != nil {
		logger.Fatal().Err(err).Msg("unable to create manager")
	}

	// SCM provider — dispatches to GitHub or GitLab based on --scm-provider flag.
	scmProvider, err := scm.NewProvider(scmProviderType, githubToken, scmAPIURL, webhookSecret)
	if err != nil {
		logger.Fatal().Err(err).Msg("unable to create SCM provider")
	}
	gitClient := scm.NewGoGitClient()

	if err := (&bundlereconciler.Reconciler{
		Client:       mgr.GetClient(),
		Translator:   newTranslator(mgr.GetConfig(), mgr.GetClient(), splitCSV(policyNamespaces), logger),
		GraphChecker: newGraphClient(mgr.GetConfig(), logger),
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
	if err := pgReconciler.SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up PolicyGateReconciler")
	}

	if err := (&psreconciler.Reconciler{
		Client:         mgr.GetClient(),
		SCM:            scmProvider,
		GitClient:      gitClient,
		HealthDetector: newHealthDetector(mgr.GetConfig(), mgr.GetClient(), logger),
		Shard:          shard,
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
		logger.Info().Str("addr", webhookBindAddress).Msg("starting webhook server")
		if err := http.ListenAndServe(webhookBindAddress, mux); err != nil {
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
		logger.Info().Str("addr", uiListenAddress).Msg("starting UI server")
		if err := http.ListenAndServe(uiListenAddress, uiMux); err != nil {
			logger.Error().Err(err).Msg("UI server error")
		}
	}()

	logger.Info().Msg("starting kardinal-controller")

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
