// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultApplierClusterRole is the ClusterRole bound to the Graph
// ServiceAccount in its namespace. It ships in the Helm chart
// (templates/graph-rbac.yaml) and grants what a kardinal Graph applies in its
// own namespace: PromotionSteps, PolicyGates, PRStatuses, MetricChecks, and
// read on Bundles, Approvals and Verdicts.
const DefaultApplierClusterRole = "kardinal-graph-applier"

// managedByLabel marks the ServiceAccount and RoleBindings the controller creates.
var managedByLabel = map[string]string{"app.kubernetes.io/managed-by": "kardinal-promoter"}

// errUnmanaged means a RoleBinding with the name the controller uses exists,
// does not carry managedByLabel, and would need a change. The controller does
// not rewrite RBAC objects it did not create.
var errUnmanaged = errors.New("exists and is not managed by kardinal-promoter")

// IdentityProvisioner ensures the ServiceAccount that kro impersonates for a
// Graph exists and is bound to exactly the ClusterRoles the Graph needs.
//
// kro applies a namespaced Graph as spec.serviceAccountName in the Graph's
// namespace and refuses to fall back to its own identity, so without this
// every apply is forbidden. RBAC is namespace-scoped: one RoleBinding of the
// applier role in the Graph namespace. A Graph reads nothing outside it.
// See docs/design/16-graph-capability-ledger.md G5.
type IdentityProvisioner struct {
	// Writer creates, updates and deletes ServiceAccounts and RoleBindings.
	Writer client.Writer
	// Reader reads them back. Use an uncached reader (mgr.GetAPIReader()) so
	// the controller does not need cluster-wide list/watch on RBAC objects.
	Reader client.Reader

	ServiceAccountName string
	ApplierClusterRole string
}

// ApplierBindingName is the name of the applier RoleBinding in every Graph
// namespace (the applier ClusterRole's name).
func (p *IdentityProvisioner) ApplierBindingName() string {
	return p.applierRole()
}

// ServiceAccount is the name of the Graph ServiceAccount in every Graph
// namespace.
func (p *IdentityProvisioner) ServiceAccount() string {
	return p.saName()
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

// Ensure provisions the identity for g: the ServiceAccount and the applier
// RoleBinding in g's namespace.
func (p *IdentityProvisioner) Ensure(ctx context.Context, g *Graph) error {
	if p == nil || g == nil {
		return nil
	}
	sa := p.saName()
	if err := p.ensureServiceAccount(ctx, g.Namespace, sa); err != nil {
		return err
	}
	_, err := p.ensureRoleBinding(ctx, g.Namespace, p.applierRole(), p.applierRole(), g.Namespace, sa)
	return err
}

func isManaged(labels map[string]string) bool {
	for k, v := range managedByLabel {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// ensureServiceAccount creates the ServiceAccount if it does not exist. An
// existing one is used as is: the ServiceAccount itself grants nothing (the
// RoleBindings do), and anyone who may create it in the namespace may already
// run pods as any ServiceAccount there.
func (p *IdentityProvisioner) ensureServiceAccount(ctx context.Context, ns, name string) error {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: ns, Labels: managedByLabel,
	}}
	if err := p.Writer.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("graph identity: create serviceaccount %s/%s: %w", ns, name, err)
	}
	return nil
}

// ensureRoleBinding creates a RoleBinding in ns that binds clusterRole to
// saNamespace/saName, or repairs the subjects of the one the controller
// created earlier. It reports whether the binding is managed by the
// controller. A binding the controller did not create is accepted if it
// already grants the role to the ServiceAccount, and otherwise left alone
// with an error wrapping errUnmanaged.
func (p *IdentityProvisioner) ensureRoleBinding(ctx context.Context,
	ns, name, clusterRole, saNamespace, saName string) (bool, error) {
	subject := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: saName, Namespace: saNamespace}
	desired := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: managedByLabel},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole,
		},
		Subjects: []rbacv1.Subject{subject},
	}
	err := p.Writer.Create(ctx, desired)
	if err == nil {
		return true, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return false, fmt.Errorf("graph identity: create rolebinding %s/%s: %w", ns, name, err)
	}

	var existing rbacv1.RoleBinding
	if err := p.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &existing); err != nil {
		return false, fmt.Errorf("graph identity: get rolebinding %s/%s: %w", ns, name, err)
	}
	if existing.RoleRef != desired.RoleRef {
		// roleRef is immutable; a binding with our name but another role is
		// not ours to rewrite.
		return false, fmt.Errorf("graph identity: rolebinding %s/%s binds %s/%s, want ClusterRole/%s",
			ns, name, existing.RoleRef.Kind, existing.RoleRef.Name, clusterRole)
	}
	managed := isManaged(existing.Labels)
	if !managed {
		for _, s := range existing.Subjects {
			if s == subject {
				return false, nil
			}
		}
		return false, fmt.Errorf("graph identity: rolebinding %s/%s %w; add subject %s/%s to it, "+
			"or delete it so the controller can create its own", ns, name, errUnmanaged, saNamespace, saName)
	}
	if len(existing.Subjects) == 1 && existing.Subjects[0] == subject {
		return true, nil
	}
	existing.Subjects = desired.Subjects
	if err := p.Writer.Update(ctx, &existing); err != nil {
		return true, fmt.Errorf("graph identity: update rolebinding %s/%s: %w", ns, name, err)
	}
	return true, nil
}
