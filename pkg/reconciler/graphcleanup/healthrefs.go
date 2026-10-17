// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graphcleanup

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/rs/zerolog"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// AnnotationReaderNamespaces is the annotation releases before #1283 put on
// the applier RoleBinding of a Graph namespace: the namespaces, comma
// separated, where they bound the reader ClusterRole for its Graphs.
const AnnotationReaderNamespaces = "kardinal.io/reader-namespaces"

// managedBy is the label the controller puts on the RoleBindings it creates.
const managedBy = "app.kubernetes.io/managed-by"

// HealthRefCleanup is a leader-only manager Runnable that removes, once at
// startup, what releases before #1283 left for the Graph health ref nodes,
// which are gone:
//
//   - the health ref nodes in existing Graphs (graph.RemoveAllHealthRefs).
//     They read through the reader ClusterRole, which the chart no longer
//     ships, and kro treats a forbidden ref read as an error, so such a Graph
//     would not reconcile;
//   - the reader RoleBindings. Without the ClusterRole they grant nothing;
//     deleting them leaves nothing a ClusterRole of that name created later
//     would bring back.
//
// It finds the RoleBindings without listing them: the applier RoleBinding of
// each namespace with a Pipeline (or the watched namespace) records them in
// AnnotationReaderNamespaces. A binding is deleted only when it is the one
// the controller made: named <reader role>-<Graph namespace>, labelled
// managed-by kardinal-promoter, binding the reader ClusterRole to the Graph
// ServiceAccount of that namespace alone. The record is then removed. A
// binding from a release before the record (#1244) is not found; the
// upgrade notes list them with a command.
type HealthRefCleanup struct {
	// Graphs removes the health ref nodes from the Graphs.
	Graphs interface {
		RemoveAllHealthRefs(ctx context.Context, namespace string) (int, error)
	}
	// Client lists Pipelines and deletes and updates RoleBindings.
	Client client.Client
	// APIReader reads RoleBindings from the API server.
	APIReader client.Reader
	// Identity names the applier RoleBinding and the Graph ServiceAccount.
	Identity *graph.IdentityProvisioner
	// Namespace, in namespace mode (--watch-namespace), is the only
	// namespace looked at.
	Namespace string
}

// NeedLeaderElection makes the cleanup run on the leader only.
func (c *HealthRefCleanup) NeedLeaderElection() bool { return true }

// Start runs the cleanup once. A failure is logged, not returned: it must
// not stop the manager, and the next start tries again.
func (c *HealthRefCleanup) Start(ctx context.Context) error {
	log := zerolog.Ctx(ctx).With().Str("runnable", "health-ref-cleanup").Logger()
	if c.Graphs != nil {
		n, err := c.Graphs.RemoveAllHealthRefs(ctx, c.Namespace)
		if err != nil {
			log.Warn().Err(err).Msg("removing the health ref nodes from Graphs failed (non-fatal); it runs again at the next start")
		} else if n > 0 {
			log.Info().Int("graphs", n).Msg("removed the health ref nodes of an earlier release from Graphs (#1283)")
		}
	}
	deleted, err := c.run(ctx, log)
	if err != nil {
		log.Warn().Err(err).Msg("reader RoleBinding cleanup failed (non-fatal); it runs again at the next start")
		return nil
	}
	if deleted > 0 {
		log.Info().Int("deleted", deleted).Msg("deleted the reader RoleBindings of the removed Graph health refs (#1283)")
	}
	return nil
}

// run deletes the recorded reader RoleBindings and returns how many it
// deleted.
func (c *HealthRefCleanup) run(ctx context.Context, log zerolog.Logger) (int, error) {
	if c.Identity == nil {
		return 0, nil
	}
	namespaces := []string{c.Namespace}
	if c.Namespace == "" {
		var pipelines v1alpha1.PipelineList
		if err := c.Client.List(ctx, &pipelines); err != nil {
			return 0, fmt.Errorf("list pipelines: %w", err)
		}
		seen := map[string]bool{}
		for _, p := range pipelines.Items {
			seen[p.Namespace] = true
		}
		namespaces = namespaces[:0]
		for ns := range seen {
			namespaces = append(namespaces, ns)
		}
		sort.Strings(namespaces)
	}
	deleted := 0
	for _, graphNS := range namespaces {
		n, err := c.cleanNamespace(ctx, log, graphNS)
		deleted += n
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// cleanNamespace deletes the reader RoleBindings recorded on graphNS's
// applier RoleBinding, then the record.
func (c *HealthRefCleanup) cleanNamespace(ctx context.Context, log zerolog.Logger, graphNS string) (int, error) {
	var applier rbacv1.RoleBinding
	err := c.APIReader.Get(ctx, client.ObjectKey{Namespace: graphNS, Name: c.Identity.ApplierBindingName()}, &applier)
	if apierrors.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get rolebinding %s/%s: %w", graphNS, c.Identity.ApplierBindingName(), err)
	}
	record, ok := applier.Annotations[AnnotationReaderNamespaces]
	if !ok {
		return 0, nil
	}
	role, ok := readerRole(applier.RoleRef.Name)
	deleted := 0
	if ok {
		for _, ns := range strings.Split(record, ",") {
			if ns = strings.TrimSpace(ns); ns == "" {
				continue
			}
			gone, err := c.deleteReader(ctx, ns, role, graphNS)
			if err != nil {
				return deleted, err
			}
			if gone {
				deleted++
				log.Info().Str("namespace", ns).Str("graphNamespace", graphNS).Msg("deleted a reader RoleBinding")
			}
		}
	} else {
		log.Warn().Str("applierRole", applier.RoleRef.Name).Str("graphNamespace", graphNS).
			Msg("the reader ClusterRole name cannot be told from the applier's; leaving its reader RoleBindings")
	}
	delete(applier.Annotations, AnnotationReaderNamespaces)
	if err := c.Client.Update(ctx, &applier); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		return deleted, fmt.Errorf("drop %s from rolebinding %s/%s: %w", AnnotationReaderNamespaces, graphNS, applier.Name, err)
	}
	return deleted, nil
}

// readerRole is the name of the reader ClusterRole that came with the
// applier ClusterRole applier: the chart named them <fullname>-graph-applier
// and <fullname>-graph-reader, the controller's defaults kardinal-graph-*.
func readerRole(applier string) (string, bool) {
	base, ok := strings.CutSuffix(applier, "-graph-applier")
	if !ok {
		return "", false
	}
	return base + "-graph-reader", true
}

// deleteReader deletes the reader RoleBinding for graphNS in ns when it is
// the one the controller created, and reports whether it did. The delete
// carries the binding's UID, so a binding recreated meanwhile is left alone.
func (c *HealthRefCleanup) deleteReader(ctx context.Context, ns, role, graphNS string) (bool, error) {
	var rb rbacv1.RoleBinding
	err := c.APIReader.Get(ctx, client.ObjectKey{Namespace: ns, Name: role + "-" + graphNS}, &rb)
	if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get rolebinding %s/%s-%s: %w", ns, role, graphNS, err)
	}
	if !isReaderBinding(&rb, role, graphNS, c.Identity.ServiceAccount()) {
		return false, nil
	}
	err = c.Client.Delete(ctx, &rb, client.Preconditions{UID: &rb.UID})
	switch {
	case err == nil:
		return true, nil
	case apierrors.IsNotFound(err) || apierrors.IsConflict(err) || apierrors.IsForbidden(err):
		return false, nil
	}
	return false, fmt.Errorf("delete rolebinding %s/%s: %w", ns, rb.Name, err)
}

// isReaderBinding reports whether rb is the reader RoleBinding the
// controller created for graphNS: managed by kardinal-promoter, binding the
// ClusterRole role to the ServiceAccount sa of graphNS alone.
func isReaderBinding(rb *rbacv1.RoleBinding, role, graphNS, sa string) bool {
	if rb.Labels[managedBy] != "kardinal-promoter" || rb.RoleRef.Kind != "ClusterRole" || rb.RoleRef.Name != role ||
		len(rb.Subjects) != 1 {
		return false
	}
	s := rb.Subjects[0]
	return s.Kind == rbacv1.ServiceAccountKind && s.Name == sa && s.Namespace == graphNS
}
