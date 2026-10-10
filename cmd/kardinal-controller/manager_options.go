// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	hookrunrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/hookrun"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
)

// auditRetentionDefault is --audit-retention's default: on. AuditEvents have
// no owner, so unbounded they fill etcd, and a full etcd quota stops the
// whole cluster, which is worse than losing records past 90 days or past a
// Pipeline's 1000 newest (the release-candidate soak: 350-660 records a
// minute, the default 2 GiB quota full in 2 to 4 days).
const auditRetentionDefault = true

// gracefulShutdownTimeout is how long the controller waits on shutdown for
// in-flight reconciles, whose context the shutdown cancels, and HTTP requests
// to return. It is half the pod's terminationGracePeriodSeconds (60s) to
// leave room for cleanup. (#574)
const gracefulShutdownTimeout = 30 * time.Second

// hookJobSelector selects the Jobs of HookRuns.
var hookJobSelector = func() labels.Selector {
	req, err := labels.NewRequirement(hookrunrecon.LabelHookRun, selection.Exists, nil)
	if err != nil {
		panic(err) // a constant key: unreachable
	}
	return labels.NewSelector().Add(*req)
}()

// managerConfig holds the flags that shape the controller-runtime manager.
type managerConfig struct {
	metricsBindAddress     string
	healthProbeBindAddress string
	leaderElect            bool
	watchNamespace         string
	// namespaceShard is --namespace-shard: each shard elects its own leader.
	namespaceShard string
	// restConfig is the controller's API server config; nil leaves the
	// leader election client to the manager's default.
	restConfig *rest.Config
}

// Leader election client rate limits (#1592): the Lease is renewed every
// 2s at most, so these never throttle it, and its own limiter means
// reconcile traffic in the process can never take its tokens. The fix for
// API Priority and Fairness pressure is the chart's FlowSchema; this only
// keeps the process-side limiter separate. controller-runtime appends
// "/leader-election" to the user agent itself.
const (
	leaderElectionQPS   = 5
	leaderElectionBurst = 10
)

// leaderElectionConfig is cfg for the leader election client alone: its
// own rate limiter, so the Lease renewal never waits behind the
// reconcilers' requests in the controller process. The fix for a busy or
// throttled API server is the chart's FlowSchema (templates/flowschema.yaml),
// which gives the Lease requests their own priority level.
func leaderElectionConfig(cfg *rest.Config) *rest.Config {
	if cfg == nil {
		return nil
	}
	c := rest.CopyConfig(cfg)
	c.QPS, c.Burst = leaderElectionQPS, leaderElectionBurst
	// A RateLimiter set on cfg would be shared with the reconcilers' client;
	// nil makes the copy build its own from QPS and Burst.
	c.RateLimiter = nil
	return c
}

// buildManagerOptions returns the options main passes to ctrl.NewManager.
//
// RecoverPanic is intentionally left unset: controller-runtime defaults it to
// true, so a panic in a Reconcile is caught, counted in the ReconcilePanics
// metric and retried with backoff. Do not set it to false; that brings back
// crash-loop-on-panic. (spec #920, docs/design/15-production-readiness.md)
//
// LeaderElectionReleaseOnCancel makes a leader that shuts down release its
// Lease, so a standby takes over at once instead of after the 15s lease
// duration. The manager releases it only after every runnable has stopped or
// the graceful shutdown timed out, and it is safe only because main exits as
// soon as mgr.Start returns: nothing reconciles after the release.
func buildManagerOptions(cfg managerConfig) ctrl.Options {
	return ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: cfg.metricsBindAddress,
		},
		HealthProbeBindAddress:        cfg.healthProbeBindAddress,
		LeaderElection:                cfg.leaderElect,
		LeaderElectionID:              leaderElectionID(cfg.namespaceShard),
		LeaderElectionReleaseOnCancel: true,
		LeaderElectionConfig:          leaderElectionConfig(cfg.restConfig),
		GracefulShutdownTimeout:       ptr(gracefulShutdownTimeout),
		Cache:                         shardCacheOpts(buildCacheOpts(cfg.watchNamespace), cfg.namespaceShard),
		Client: sigs_client.Options{
			Cache: &sigs_client.CacheOptions{DisableFor: uncachedObjects()},
		},
	}
}

// leaderElectionID is the leader election Lease name: one per shard, so
// every shard has its own leader.
func leaderElectionID(shard string) string {
	if shard == "" {
		return "kardinal-promoter-leader"
	}
	return "kardinal-promoter-leader-" + shard
}

// buildCacheOpts limits the informer cache to watchNamespace when it is set.
// This is the mechanism behind namespace-scoped install mode, where the Helm
// chart renders a Role/RoleBinding instead of a ClusterRole/ClusterRoleBinding.
// (docs/design/15-production-readiness.md §Lens 6)
//
// Jobs are cached only when they carry the kardinal.io/hookrun label: the
// HookRun reconciler owns those (hook Jobs), and caching every Job in the
// cluster would hold them all in memory for nothing.
func buildCacheOpts(watchNamespace string) cache.Options {
	opts := cache.Options{
		ByObject: map[sigs_client.Object]cache.ByObject{
			&batchv1.Job{}: {Label: hookJobSelector},
		},
	}
	if watchNamespace != "" {
		opts.DefaultNamespaces = map[string]cache.Config{watchNamespace: {}}
	}
	return opts
}

// shardCacheOpts limits the Lease cache of a sharded controller to the
// shard tokens (shard.CacheByObject): without it the shard gate's reads
// would cache every Lease in the cluster, leader election Leases included.
func shardCacheOpts(opts cache.Options, namespaceShard string) cache.Options {
	if namespaceShard != "" {
		if opts.ByObject == nil {
			opts.ByObject = map[sigs_client.Object]cache.ByObject{}
		}
		// Added to, not replaced: the hook Job selector stays.
		for obj, by := range shard.CacheByObject() {
			opts.ByObject[obj] = by
		}
	}
	return opts
}

// shardCallTimeout is the HTTP timeout of the shard gate's clients: every
// call of a Lease pass ends well before the 10 s heartbeat renewal, so a
// hung API server cannot stall a pass (pkg/shard fences on its own ticker
// regardless).
const shardCallTimeout = 5 * time.Second

// shardClients returns the shard gate's clients: reads of Namespaces and
// tokens through the manager's cache, writes and the uncached reads
// (heartbeats, the token re-read after a fence) straight to the API server,
// all with shardCallTimeout. With sharding off it returns the manager's.
func shardClients(mgr ctrl.Manager, namespaceShard string) (sigs_client.Client, sigs_client.Reader, error) {
	if namespaceShard == "" {
		return mgr.GetClient(), mgr.GetAPIReader(), nil
	}
	cfg := rest.CopyConfig(mgr.GetConfig())
	cfg.Timeout = shardCallTimeout
	c, err := sigs_client.New(cfg, sigs_client.Options{Scheme: mgr.GetScheme(),
		Cache: &sigs_client.CacheOptions{Reader: mgr.GetCache()}})
	if err != nil {
		return nil, nil, fmt.Errorf("shard client: %w", err)
	}
	r, err := sigs_client.New(cfg, sigs_client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		return nil, nil, fmt.Errorf("shard API reader: %w", err)
	}
	return c, r, nil
}

// uncachedObjects are the types the manager client reads straight from the
// API server instead of through an informer.
//
//   - Secret: SCM token rotation and Pipeline git secrets. A cached read would
//     start an informer holding every Secret in the cluster in memory, and in
//     namespace-scoped mode it fails for a Secret outside --watch-namespace
//     (the SCM token Secret lives in the controller namespace).
//   - ConfigMap: the kardinal-version ConfigMap in the controller namespace,
//     which has the same namespace-scoped problem.
//   - Event: the UI step events endpoint filters on involvedObject.name. The
//     API server supports that field selector; the cache does not, so a cached
//     read listed every Event in the namespace (and started a cluster-wide
//     Event informer) to filter them in memory.
func uncachedObjects() []sigs_client.Object {
	return []sigs_client.Object{&corev1.Secret{}, &corev1.ConfigMap{}, &corev1.Event{}}
}
