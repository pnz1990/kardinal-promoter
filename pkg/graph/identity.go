// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Default ClusterRole names bound to the Graph ServiceAccount. Both ship in
// the Helm chart (templates/graph-rbac.yaml).
const (
	// DefaultApplierClusterRole grants what a kardinal Graph applies in its own
	// namespace: PromotionSteps, PolicyGates, PRStatuses, and read on Bundles.
	DefaultApplierClusterRole = "kardinal-graph-applier"
	// DefaultReaderClusterRole grants read/watch on the health kinds a Graph
	// references (Deployments, Argo CD Applications, Flux Kustomizations,
	// Argo Rollouts, Flagger Canaries).
	DefaultReaderClusterRole = "kardinal-graph-reader"
)

// managedByLabel marks the ServiceAccount and RoleBindings the controller creates.
var managedByLabel = map[string]string{"app.kubernetes.io/managed-by": "kardinal-promoter"}

// IdentityProvisioner ensures the ServiceAccount that kro impersonates for a
// Graph exists and is bound to exactly the ClusterRoles the Graph needs.
//
// kro applies a namespaced Graph as spec.serviceAccountName in the Graph's
// namespace and refuses to fall back to its own identity, so without this
// every apply is forbidden. RBAC is namespace-scoped (RoleBindings only):
// the applier role in the Graph namespace, the reader role in each namespace
// a health ref points into. See docs/design/16-graph-capability-ledger.md G5.
type IdentityProvisioner struct {
	// Writer creates and updates ServiceAccounts and RoleBindings.
	Writer client.Writer
	// Reader reads them back. Use an uncached reader (mgr.GetAPIReader()) so
	// the controller does not need cluster-wide list/watch on RBAC objects.
	Reader client.Reader

	ServiceAccountName string
	ApplierClusterRole string
	ReaderClusterRole  string
}

func (p *IdentityProvisioner) saName() string {
	if p.ServiceAccountName == "" {
		return DefaultGraphServiceAccount
	}
	return p.ServiceAccountName
}

func (p *IdentityProvisioner) applierRole() string {
	if p.ApplierClusterRole == "" {
		return DefaultApplierClusterRole
	}
	return p.ApplierClusterRole
}

func (p *IdentityProvisioner) readerRole() string {
	if p.ReaderClusterRole == "" {
		return DefaultReaderClusterRole
	}
	return p.ReaderClusterRole
}

// Ensure provisions the identity for g: the ServiceAccount and applier
// RoleBinding in g's namespace, and a reader RoleBinding in every namespace
// that one of g's ref nodes reads from. A reader namespace that does not
// exist is skipped — there is nothing to read there yet.
func (p *IdentityProvisioner) Ensure(ctx context.Context, g *Graph) error {
	if p == nil || g == nil {
		return nil
	}
	ns := g.Namespace
	sa := p.saName()

	if err := p.ensureServiceAccount(ctx, ns, sa); err != nil {
		return err
	}
	if err := p.ensureRoleBinding(ctx, ns, p.applierRole(), p.applierRole(), ns, sa); err != nil {
		return err
	}
	for _, readNS := range RefNamespaces(g) {
		name := p.readerRole() + "-" + ns
		err := p.ensureRoleBinding(ctx, readNS, name, p.readerRole(), ns, sa)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (p *IdentityProvisioner) ensureServiceAccount(ctx context.Context, ns, name string) error {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: ns, Labels: managedByLabel,
	}}
	if err := p.Writer.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("graph identity: create serviceaccount %s/%s: %w", ns, name, err)
	}
	return nil
}

// ensureRoleBinding creates (or repairs the subjects of) a RoleBinding in ns
// that binds clusterRole to saNamespace/saName.
func (p *IdentityProvisioner) ensureRoleBinding(ctx context.Context,
	ns, name, clusterRole, saNamespace, saName string) error {
	subjects := []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: saName, Namespace: saNamespace}}
	desired := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: managedByLabel},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole,
		},
		Subjects: subjects,
	}
	err := p.Writer.Create(ctx, desired)
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("graph identity: create rolebinding %s/%s: %w", ns, name, err)
	}

	var existing rbacv1.RoleBinding
	if err := p.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &existing); err != nil {
		return fmt.Errorf("graph identity: get rolebinding %s/%s: %w", ns, name, err)
	}
	if existing.RoleRef != desired.RoleRef {
		// roleRef is immutable; a binding with our name but another role is
		// not ours to rewrite.
		return fmt.Errorf("graph identity: rolebinding %s/%s binds %s/%s, want ClusterRole/%s",
			ns, name, existing.RoleRef.Kind, existing.RoleRef.Name, clusterRole)
	}
	if len(existing.Subjects) == 1 && existing.Subjects[0] == subjects[0] {
		return nil
	}
	existing.Subjects = subjects
	if err := p.Writer.Update(ctx, &existing); err != nil {
		return fmt.Errorf("graph identity: update rolebinding %s/%s: %w", ns, name, err)
	}
	return nil
}

// RefNamespaces returns the sorted, de-duplicated namespaces that g's ref
// nodes read from. A ref without a namespace reads from g's namespace.
func RefNamespaces(g *Graph) []string {
	seen := map[string]bool{}
	for _, n := range g.Spec.Nodes {
		if n.Ref == nil {
			continue
		}
		ns := g.Namespace
		if md, ok := n.Ref["metadata"].(map[string]interface{}); ok {
			if v, ok := md["namespace"].(string); ok && v != "" {
				ns = v
			}
		}
		seen[ns] = true
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}
