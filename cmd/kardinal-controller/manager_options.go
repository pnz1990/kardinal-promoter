// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// gracefulShutdownTimeout lets in-flight reconciles and HTTP requests finish
// before the controller exits. It is half the pod's
// terminationGracePeriodSeconds (60s) to leave room for cleanup. (#574)
const gracefulShutdownTimeout = 30 * time.Second

// managerConfig holds the flags that shape the controller-runtime manager.
type managerConfig struct {
	metricsBindAddress     string
	healthProbeBindAddress string
	leaderElect            bool
	watchNamespace         string
}

// buildManagerOptions returns the options main passes to ctrl.NewManager.
//
// RecoverPanic is intentionally left unset: controller-runtime defaults it to
// true, so a panic in a Reconcile is caught, counted in the ReconcilePanics
// metric and retried with backoff. Do not set it to false; that brings back
// crash-loop-on-panic. (spec #920, docs/design/15-production-readiness.md)
func buildManagerOptions(cfg managerConfig) ctrl.Options {
	return ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: cfg.metricsBindAddress,
		},
		HealthProbeBindAddress:  cfg.healthProbeBindAddress,
		LeaderElection:          cfg.leaderElect,
		LeaderElectionID:        "kardinal-promoter-leader",
		GracefulShutdownTimeout: ptr(gracefulShutdownTimeout),
		Cache:                   buildCacheOpts(cfg.watchNamespace),
		Client: sigs_client.Options{
			Cache: &sigs_client.CacheOptions{DisableFor: uncachedObjects()},
		},
	}
}

// buildCacheOpts limits the informer cache to watchNamespace when it is set.
// This is the mechanism behind namespace-scoped install mode, where the Helm
// chart renders a Role/RoleBinding instead of a ClusterRole/ClusterRoleBinding.
// (docs/design/15-production-readiness.md §Lens 6)
func buildCacheOpts(watchNamespace string) cache.Options {
	opts := cache.Options{}
	if watchNamespace != "" {
		opts.DefaultNamespaces = map[string]cache.Config{watchNamespace: {}}
	}
	return opts
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
