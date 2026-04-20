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

// Package main is the entry point for the kardinal-agent binary.
//
// kardinal-agent runs in a spoke (workload) cluster and handles PromotionSteps
// that carry a matching `kardinal.io/shard` label. The control-plane
// kardinal-controller assigns shard labels during Pipeline-to-Graph translation;
// the agent uses them to claim only its own work.
//
// The agent is intentionally minimal:
//   - Only the PromotionStep reconciler is registered.
//   - No Bundle, Pipeline, PolicyGate, or UI components.
//   - SCM credentials are read from the local cluster (not the control plane).
//
// See docs/design/07-distributed-architecture.md for the full architecture.
package main

import (
	"flag"
	"os"

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
	healthpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	psreconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"

	// Import built-in steps to register them via init().
	_ "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

// AgentVersion is the agent version string, overridable at build time via ldflags.
//
//nolint:gochecknoglobals // Build-time constant, analogous to ControllerVersion in kardinal-controller.
var AgentVersion = "v0.1.0-dev"

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kardinalv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		shard                  string
		zerologLevel           string
		metricsBindAddress     string
		healthProbeBindAddress string
		leaderElect            bool
		githubToken            string
		scmProviderType        string
		scmAPIURL              string
		webhookSecret          string
	)

	flag.StringVar(&shard, "shard", os.Getenv("KARDINAL_SHARD"),
		"[Required] Shard name for this agent. The agent only processes PromotionSteps "+
			"with kardinal.io/shard=<shard>. Must not be empty.")
	flag.StringVar(&zerologLevel, "log-level", "info",
		"Log level for zerolog. One of: debug, info, warn, error.")
	flag.StringVar(&metricsBindAddress, "metrics-bind-address", ":8085",
		"The address the metric endpoint binds to. Defaults to :8085 to avoid "+
			"port collision with kardinal-controller (:8080) during co-location testing.")
	flag.StringVar(&healthProbeBindAddress, "health-probe-bind-address", ":8086",
		"The address the health probe endpoint binds to. Defaults to :8086 to avoid "+
			"port collision with kardinal-controller (:8081) during co-location testing.")
	flag.BoolVar(&leaderElect, "leader-elect", false,
		"Enable leader election for the agent. Typically not needed — one agent per shard.")
	flag.StringVar(&githubToken, "github-token", os.Getenv("GITHUB_TOKEN"),
		"SCM token for API operations (GitHub PAT or GitLab private token). "+
			"Stored in the spoke cluster, not the control plane.")
	flag.StringVar(&scmProviderType, "scm-provider", os.Getenv("KARDINAL_SCM_PROVIDER"),
		"SCM provider type: \"github\" (default) or \"gitlab\".")
	flag.StringVar(&scmAPIURL, "scm-api-url", os.Getenv("KARDINAL_SCM_API_URL"),
		"SCM API base URL override (e.g. for GitHub Enterprise or self-managed GitLab).")
	flag.StringVar(&webhookSecret, "webhook-secret", os.Getenv("KARDINAL_WEBHOOK_SECRET"),
		"Webhook secret — unused by the agent but accepted to allow identical env injection with the controller.")

	opts := czap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	// Configure zerolog level.
	level, err := zerolog.ParseLevel(zerologLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()

	// O3: shard is required. An agent without a shard would compete with the controller.
	if err := validateShard(shard); err != nil {
		logger.Fatal().Err(err).Msg("[kardinal-agent] --shard is required for distributed mode")
	}

	logger.Info().
		Str("shard", shard).
		Str("version", AgentVersion).
		Msg("[kardinal-agent] starting")

	ctrl.SetLogger(czap.New(czap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsBindAddress,
		},
		HealthProbeBindAddress: healthProbeBindAddress,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "kardinal-agent-" + shard,
	})
	if err != nil {
		logger.Fatal().Err(err).Msg("[kardinal-agent] unable to create manager")
	}

	// SCM provider — same as controller but credentials come from the spoke cluster.
	scmProvider, err := scm.NewProvider(scmProviderType, githubToken, scmAPIURL, webhookSecret)
	if err != nil {
		logger.Fatal().Err(err).Msg("[kardinal-agent] unable to create SCM provider")
	}
	gitClient := scm.NewGoGitClient()

	// O4: Only register the PromotionStep reconciler, with the shard value.
	if err := (&psreconciler.Reconciler{
		Client:         mgr.GetClient(),
		SCM:            scmProvider,
		GitClient:      gitClient,
		HealthDetector: newHealthDetector(mgr.GetConfig(), mgr.GetClient(), logger),
		Shard:          shard,
	}).SetupWithManager(mgr); err != nil {
		logger.Fatal().Err(err).Msg("[kardinal-agent] unable to set up PromotionStepReconciler")
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Fatal().Err(err).Msg("[kardinal-agent] unable to set up health check")
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Fatal().Err(err).Msg("[kardinal-agent] unable to set up ready check")
	}

	logger.Info().
		Str("shard", shard).
		Str("metrics", metricsBindAddress).
		Str("health", healthProbeBindAddress).
		Msg("[kardinal-agent] manager started — watching PromotionSteps for shard")

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Fatal().Err(err).Msg("[kardinal-agent] problem running manager")
	}
}

// validateShard returns an error if shard is empty or whitespace-only.
// This is a separate function so it can be unit-tested without starting the manager.
func validateShard(shard string) error {
	if len(shard) == 0 {
		return errShardRequired
	}
	for _, r := range shard {
		if r != ' ' && r != '\t' {
			return nil
		}
	}
	return errShardRequired
}

// errShardRequired is returned when --shard is not provided.
var errShardRequired = shardError("--shard is required: the agent must have a shard name to filter PromotionSteps")

type shardError string

func (e shardError) Error() string { return string(e) }

// newHealthDetector constructs an AutoDetector for health checking using dynamic client.
func newHealthDetector(cfg *rest.Config, k8s sigs_client.Client, log zerolog.Logger) *healthpkg.AutoDetector {
	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("[kardinal-agent] unable to create dynamic client for health detection")
	}
	return healthpkg.NewAutoDetector(k8s, dynClient)
}
