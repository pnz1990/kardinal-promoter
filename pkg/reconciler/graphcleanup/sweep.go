// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graphcleanup

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// SweepInterval is how often Sweep looks for leaked reader RoleBindings.
const SweepInterval = 10 * time.Minute

// Sweep is a leader-only manager Runnable that deletes the reader RoleBindings
// the controller created for a namespace that is gone or holds no kardinal
// Graph reading through them. It runs at startup and then every Interval.
//
// The Reconciler prunes when a Graph is deleted; Sweep catches what it cannot
// see: Graphs deleted while the controller was down, bindings left by an older
// controller version that never pruned on delete, and, with
// --graph-reader-namespaces=*, bindings in namespaces the applier RoleBinding's
// record no longer lists because its namespace is gone.
//
// It lists, cluster-wide, the RoleBindings that carry the controller's
// managed-by label and touches only the ones IdentityProvisioner.ReaderBinding
// recognizes. It needs list on RoleBindings; in namespace mode
// (--watch-namespace) the controller has no cluster-wide RBAC and does not run
// it.
type Sweep struct {
	// APIReader lists RoleBindings from the API server.
	APIReader client.Reader
	// Graphs lists the Graphs of a namespace.
	Graphs GraphLister
	// Identity recognizes and prunes reader RoleBindings.
	Identity *graph.IdentityProvisioner
	// Interval between sweeps; SweepInterval when zero.
	Interval time.Duration
}

// NeedLeaderElection makes Sweep run on the leader only.
func (s *Sweep) NeedLeaderElection() bool { return true }

// Start runs a sweep now and then every Interval until ctx is done. A failed
// sweep is logged and retried at the next tick.
func (s *Sweep) Start(ctx context.Context) error {
	interval := s.Interval
	if interval <= 0 {
		interval = SweepInterval
	}
	log := zerolog.Ctx(ctx).With().Str("runnable", "graph-reader-sweep").Logger()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.Run(log.WithContext(ctx)); err != nil {
			log.Warn().Err(err).Msg("graph identity: reader rolebinding sweep failed")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Run sweeps once. Every Graph namespace with a reader binding is pruned with
// the bindings found for it, so a binding the applier RoleBinding's record
// does not list is still found.
func (s *Sweep) Run(ctx context.Context) error {
	if s.Identity == nil {
		return nil
	}
	var list rbacv1.RoleBindingList
	if err := s.APIReader.List(ctx, &list, client.MatchingLabels{"app.kubernetes.io/managed-by": "kardinal-promoter"}); err != nil {
		return fmt.Errorf("list rolebindings: %w", err)
	}
	found := map[string][]string{} // Graph namespace -> namespaces holding a reader binding for it
	for i := range list.Items {
		rb := &list.Items[i]
		if graphNS, ok := s.Identity.ReaderBinding(rb); ok {
			found[graphNS] = append(found[graphNS], rb.Namespace)
		}
	}
	graphNamespaces := make([]string, 0, len(found))
	for ns := range found {
		graphNamespaces = append(graphNamespaces, ns)
	}
	sort.Strings(graphNamespaces)

	log := zerolog.Ctx(ctx)
	var firstErr error
	for _, graphNS := range graphNamespaces {
		if err := s.prune(ctx, graphNS, found[graphNS]); err != nil {
			log.Warn().Err(err).Str("graphNamespace", graphNS).Msg("graph identity: sweep could not prune")
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	log.Debug().Int("graphNamespaces", len(graphNamespaces)).Msg("graph identity: reader rolebinding sweep done")
	return firstErr
}

// prune prunes the reader bindings for graphNS, looking in namespaces too.
func (s *Sweep) prune(ctx context.Context, graphNS string, namespaces []string) error {
	s.Identity.Lock()
	defer s.Identity.Unlock()
	graphs, err := s.Graphs.List(ctx, graphNS)
	if err != nil {
		return fmt.Errorf("list graphs: %w", err)
	}
	return s.Identity.PruneIn(ctx, graphNS, graphs, namespaces)
}
