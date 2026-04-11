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
	"flag"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/runtime"
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
	celpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/cel"
	graphpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	healthpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	bundlereconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	metriccheckrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/metriccheck"
	pipelinereconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
	policygaterecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
	psreconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	prstatusrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
	rbprecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/rollbackpolicy"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
	"github.com/kardinal-promoter/kardinal-promoter/web"

	// Import built-in steps to register them via init().
	_ "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

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

	ctrl.SetLogger(czap.New(czap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsBindAddress,
		},
		HealthProbeBindAddress: healthProbeBindAddress,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "kardinal-promoter-leader",
	})
	if err != nil {
		logger.Fatal().Err(err).Msg("unable to create manager")
	}

	// SCM provider — dispatches to GitHub or GitLab based on --scm-provider flag.
	scmProvider, err := scm.NewProvider(scmProviderType, githubToken, scmAPIURL, webhookSecret)
	if err != nil {
		logger.Fatal().Err(err).Msg("unable to create SCM provider")
	}
	gitClient := scm.NewExecGitClient()

	if err := (&bundlereconciler.Reconciler{
		Client:     mgr.GetClient(),
		Translator: newTranslator(mgr.GetConfig(), mgr.GetClient(), splitCSV(policyNamespaces), logger),
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up BundleReconciler")
	}

	if err := (&pipelinereconciler.Reconciler{Client: mgr.GetClient()}).
		SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up PipelineReconciler")
	}

	celEnv, err := celpkg.NewCELEnvironment()
	if err != nil {
		logger.Fatal().Err(err).Msg("unable to create CEL environment")
	}
	if err := (&policygaterecon.Reconciler{
		Client:    mgr.GetClient(),
		Evaluator: celpkg.NewEvaluator(celEnv),
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("unable to set up PolicyGateReconciler")
	}

	if err := (&psreconciler.Reconciler{
		Client:         mgr.GetClient(),
		SCM:            scmProvider,
		GitClient:      gitClient,
		HealthDetector: newHealthDetector(mgr.GetConfig(), mgr.GetClient(), logger),
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
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Fatal().Err(err).Msg("problem running manager")
	}
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
