// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Default ClusterRole names bound to the Graph ServiceAccount. Both ship in
// the Helm chart (templates/graph-rbac.yaml).
const (
	// DefaultApplierClusterRole grants what a kardinal Graph applies in its own
	// namespace: PromotionSteps, PolicyGates, PRStatuses, MetricChecks, and read
	// on Bundles.
	DefaultApplierClusterRole = "kardinal-graph-applier"
	// DefaultReaderClusterRole grants read/watch on the health kinds a Graph
	// references (Deployments, Argo CD Applications, Flux Kustomizations,
	// Argo Rollouts, Flagger Canaries).
	DefaultReaderClusterRole = "kardinal-graph-reader"
)

// managedByLabel marks the ServiceAccount and RoleBindings the controller creates.
var managedByLabel = map[string]string{"app.kubernetes.io/managed-by": "kardinal-promoter"}

// AnnotationReaderNamespaces, on the applier RoleBinding in a Graph namespace,
// lists (comma-separated) the namespaces holding a reader RoleBinding for that
// Graph namespace. Prune reads it to find the bindings without listing
// RoleBindings cluster-wide.
const AnnotationReaderNamespaces = "kardinal.io/reader-namespaces"

// AllNamespaces, as a ReaderNamespaces entry, allows a reader RoleBinding in
// every namespace except the Kubernetes system namespaces. Use it only when
// every Pipeline author may read every namespace (a single-tenant cluster).
const AllNamespaces = "*"

// DefaultReaderNamespaces are the namespaces the controller binds the reader
// role in by default, besides the Graph's own: where the argocd and flux health
// types look for their objects.
var DefaultReaderNamespaces = []string{"argocd", "flux-system"}

// systemNamespaces never get a reader RoleBinding, even with AllNamespaces.
var systemNamespaces = map[string]bool{"kube-system": true, "kube-public": true, "kube-node-lease": true}

// errUnmanaged means a RoleBinding with the name the controller uses exists,
// does not carry managedByLabel, and would need a change. The controller does
// not rewrite RBAC objects it did not create.
var errUnmanaged = errors.New("exists and is not managed by kardinal-promoter")

// IdentityProvisioner ensures the ServiceAccount that kro impersonates for a
// Graph exists and is bound to exactly the ClusterRoles the Graph needs.
//
// kro applies a namespaced Graph as spec.serviceAccountName in the Graph's
// namespace and refuses to fall back to its own identity, so without this
// every apply is forbidden. RBAC is namespace-scoped (RoleBindings only):
// the applier role in the Graph namespace, the reader role in each allowed
// namespace a health ref points into. See docs/design/16-graph-capability-ledger.md G5.
type IdentityProvisioner struct {
	// Writer creates, updates and deletes ServiceAccounts and RoleBindings.
	Writer client.Writer
	// Reader reads them back. Use an uncached reader (mgr.GetAPIReader()) so
	// the controller does not need cluster-wide list/watch on RBAC objects.
	Reader client.Reader

	ServiceAccountName string
	ApplierClusterRole string
	ReaderClusterRole  string

	// ReaderNamespaces lists the namespaces, besides the Graph's own, where
	// the reader role may be bound. Pipeline authors choose the namespaces
	// their health refs read, so this list is what stops one tenant from
	// granting its Graph ServiceAccount read access to another tenant's
	// namespace. Empty allows only the Graph's own namespace; AllNamespaces
	// allows every namespace except kube-system, kube-public and
	// kube-node-lease, which are never allowed.
	ReaderNamespaces []string

	// OnlyNamespace, when set (namespace mode, --watch-namespace), is the only
	// namespace the controller has RBAC in. Prune looks for reader RoleBindings
	// there only: the controller cannot have created one anywhere else, and
	// every read elsewhere is forbidden.
	OnlyNamespace string

	// mu is held through Lock and Unlock.
	mu sync.Mutex
}

// Lock serializes the callers that bind and prune reader RoleBindings. The
// translator holds it from Ensure until its Graph is created; every Prune
// caller holds it from listing the namespace's Graphs until Prune returns.
// Without it a Prune that listed the Graphs just before a new Graph was
// created could delete the reader binding Ensure had just made for it. A nil
// provisioner does nothing.
func (p *IdentityProvisioner) Lock() {
	if p != nil {
		p.mu.Lock()
	}
}

// Unlock releases Lock.
func (p *IdentityProvisioner) Unlock() {
	if p != nil {
		p.mu.Unlock()
	}
}

// ApplierBindingName is the name of the applier RoleBinding in every Graph
// namespace (the applier ClusterRole's name).
func (p *IdentityProvisioner) ApplierBindingName() string {
	return p.applierRole()
}

// ReaderBinding reports whether rb is a reader RoleBinding the controller
// created, and for Graphs in which namespace: it carries the managed-by label,
// binds the reader ClusterRole, and grants it to the Graph ServiceAccount of
// one namespace, after which it is named.
func (p *IdentityProvisioner) ReaderBinding(rb *rbacv1.RoleBinding) (graphNS string, ok bool) {
	if !isManaged(rb.Labels) || rb.RoleRef.Kind != "ClusterRole" || rb.RoleRef.Name != p.readerRole() ||
		len(rb.Subjects) != 1 {
		return "", false
	}
	s := rb.Subjects[0]
	if s.Kind != rbacv1.ServiceAccountKind || s.Name != p.saName() || rb.Name != p.readerBindingName(s.Namespace) {
		return "", false
	}
	return s.Namespace, true
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

// readerBindingName is the name of the reader RoleBinding for Graphs in graphNS.
func (p *IdentityProvisioner) readerBindingName(graphNS string) string {
	return p.readerRole() + "-" + graphNS
}

// MayRead reports whether a Graph in graphNS may get a reader RoleBinding in
// ns. A nil provisioner (identity managed outside the controller) allows
// every namespace.
func (p *IdentityProvisioner) MayRead(graphNS, ns string) bool {
	if p == nil || ns == graphNS {
		return true
	}
	if systemNamespaces[ns] {
		return false
	}
	for _, allowed := range p.ReaderNamespaces {
		if allowed == AllNamespaces || allowed == ns {
			return true
		}
	}
	return false
}

// Ensure provisions the identity for g: the ServiceAccount and applier
// RoleBinding in g's namespace, and a reader RoleBinding in every namespace
// that one of g's ref nodes reads from.
//
// It returns the namespaces it did not bind the reader role in: MayRead
// refuses them, the namespace does not exist yet (Argo CD often creates it on
// first sync), the controller may not create RoleBindings there, or a
// RoleBinding of the same name exists that the controller does not manage.
// The caller must drop g's refs into those namespaces before creating g: kro
// treats a forbidden ref read as a hard error and would degrade the Graph. The
// next translation tries again. A reader binding refused because g's own
// namespace is being deleted is an error instead: g cannot be created there
// either.
//
// Reader bindings the controller created are recorded on the applier
// RoleBinding (AnnotationReaderNamespaces) for Prune.
func (p *IdentityProvisioner) Ensure(ctx context.Context, g *Graph) ([]string, error) {
	if p == nil || g == nil {
		return nil, nil
	}
	log := zerolog.Ctx(ctx)
	ns := g.Namespace
	sa := p.saName()

	if err := p.ensureServiceAccount(ctx, ns, sa); err != nil {
		return nil, err
	}
	if _, err := p.ensureRoleBinding(ctx, ns, p.applierRole(), p.applierRole(), ns, sa); err != nil {
		return nil, err
	}
	var managed, unbound []string
	for _, readNS := range RefNamespaces(g) {
		if !p.MayRead(ns, readNS) {
			log.Warn().Str("namespace", readNS).Str("graphNamespace", ns).
				Msg("graph identity: namespace is not in --graph-reader-namespaces; not binding the reader role")
			unbound = append(unbound, readNS)
			continue
		}
		owned, err := p.ensureRoleBinding(ctx, readNS, p.readerBindingName(ns), p.readerRole(), ns, sa)
		switch {
		case err == nil:
			if owned {
				managed = append(managed, readNS)
			}
		case readNS == ns && apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause):
			// The Graph's own namespace is being deleted, so the Graph cannot
			// be created either: stop, rather than report the role unbound.
			return nil, err
		case apierrors.IsNotFound(err) || apierrors.IsForbidden(err) || errors.Is(err, errUnmanaged):
			log.Warn().Err(err).Str("namespace", readNS).Str("graphNamespace", ns).
				Msg("graph identity: reader role not bound")
			unbound = append(unbound, readNS)
		default:
			return nil, err
		}
	}
	if err := p.updateRecord(ctx, ns, managed, nil); err != nil {
		return nil, err
	}
	return unbound, nil
}

// Prune deletes the reader RoleBindings for graphNS that no Graph in graphs
// reads through any more. graphs must be every Graph in graphNS. Only
// bindings recorded by Ensure and still carrying managedByLabel are deleted.
// When the applier RoleBinding, which holds the record, is gone (its
// namespace was deleted, or someone deleted it), the bindings are looked for
// in graphNS and every namespace ReaderNamespaces names; with AllNamespaces
// the others are left for the sweep (PruneIn). With OnlyNamespace set, only
// that namespace is looked in. A namespace whose binding cannot be read stays
// in the record, so a later Prune tries again. The caller must hold Lock.
func (p *IdentityProvisioner) Prune(ctx context.Context, graphNS string, graphs []*Graph) error {
	return p.PruneIn(ctx, graphNS, graphs, nil)
}

// PruneIn is Prune that also looks for reader bindings for graphNS in
// namespaces, which the sweep passes when it found bindings there that the
// record may not list.
func (p *IdentityProvisioner) PruneIn(ctx context.Context, graphNS string, graphs []*Graph, namespaces []string) error {
	if p == nil {
		return nil
	}
	needed := map[string]bool{}
	for _, g := range graphs {
		for _, ns := range RefNamespaces(g) {
			needed[ns] = true
		}
	}
	recorded, found, err := p.recorded(ctx, graphNS)
	if err != nil {
		return err
	}
	candidates := map[string]bool{}
	for _, ns := range append(recorded, namespaces...) {
		candidates[ns] = true
	}
	if !found {
		candidates[graphNS] = true
		for _, ns := range p.ReaderNamespaces {
			if ns != AllNamespaces {
				candidates[ns] = true
			}
		}
	}
	var removed []string
	for _, ns := range sortedKeys(candidates) {
		if needed[ns] || (p.OnlyNamespace != "" && ns != p.OnlyNamespace) {
			continue
		}
		pruned, err := p.deleteReaderBinding(ctx, ns, graphNS)
		if err != nil {
			return err
		}
		if pruned {
			removed = append(removed, ns)
		}
	}
	if !found {
		return nil
	}
	return p.updateRecord(ctx, graphNS, nil, removed)
}

// deleteReaderBinding deletes the reader RoleBinding for graphNS in ns if it
// is managed by the controller, and reports whether ns holds no such binding
// any more: deleted, missing, or one the controller does not manage. A
// binding the controller may not read (ReaderNamespaces naming a namespace the
// chart grants nothing in) is logged and reported as not pruned, so its
// namespace stays in the record and a later Prune tries again.
func (p *IdentityProvisioner) deleteReaderBinding(ctx context.Context, ns, graphNS string) (bool, error) {
	var rb rbacv1.RoleBinding
	key := client.ObjectKey{Namespace: ns, Name: p.readerBindingName(graphNS)}
	if err := p.Reader.Get(ctx, key, &rb); err != nil {
		switch {
		case apierrors.IsNotFound(err):
			return true, nil
		case apierrors.IsForbidden(err):
			zerolog.Ctx(ctx).Warn().Err(err).Str("namespace", ns).Str("graphNamespace", graphNS).
				Msg("graph identity: may not read the reader rolebinding; not pruning it")
			return false, nil
		}
		return false, fmt.Errorf("graph identity: get rolebinding %s: %w", key, err)
	}
	if !isManaged(rb.Labels) || rb.RoleRef.Name != p.readerRole() {
		return true, nil
	}
	err := p.Writer.Delete(ctx, &rb, client.Preconditions{UID: &rb.UID, ResourceVersion: &rb.ResourceVersion})
	if err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("graph identity: delete rolebinding %s: %w", key, err)
	}
	zerolog.Ctx(ctx).Info().Str("rolebinding", key.String()).Str("graphNamespace", graphNS).
		Msg("graph identity: deleted a reader rolebinding no Graph reads through")
	return true, nil
}

// recorded returns the reader namespaces recorded on the applier RoleBinding,
// and whether that binding exists.
func (p *IdentityProvisioner) recorded(ctx context.Context, graphNS string) ([]string, bool, error) {
	var rb rbacv1.RoleBinding
	key := client.ObjectKey{Namespace: graphNS, Name: p.applierRole()}
	if err := p.Reader.Get(ctx, key, &rb); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("graph identity: get rolebinding %s: %w", key, err)
	}
	return splitNamespaces(rb.Annotations[AnnotationReaderNamespaces]), true, nil
}

// updateRecord adds and removes namespaces in the applier RoleBinding's
// AnnotationReaderNamespaces. It does nothing when the set does not change,
// when the binding is not managed by the controller, or, when it only
// removes, when the binding is gone.
func (p *IdentityProvisioner) updateRecord(ctx context.Context, graphNS string, add, remove []string) error {
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var rb rbacv1.RoleBinding
		key := client.ObjectKey{Namespace: graphNS, Name: p.applierRole()}
		if err := p.Reader.Get(ctx, key, &rb); err != nil {
			if apierrors.IsNotFound(err) && len(add) == 0 {
				return nil
			}
			return fmt.Errorf("graph identity: get rolebinding %s: %w", key, err)
		}
		if !isManaged(rb.Labels) {
			return nil
		}
		old := rb.Annotations[AnnotationReaderNamespaces]
		set := map[string]bool{}
		for _, ns := range splitNamespaces(old) {
			set[ns] = true
		}
		for _, ns := range add {
			set[ns] = true
		}
		for _, ns := range remove {
			delete(set, ns)
		}
		value := strings.Join(sortedKeys(set), ",")
		if value == old {
			return nil
		}
		if rb.Annotations == nil {
			rb.Annotations = map[string]string{}
		}
		if value == "" {
			delete(rb.Annotations, AnnotationReaderNamespaces)
		} else {
			rb.Annotations[AnnotationReaderNamespaces] = value
		}
		if err := p.Writer.Update(ctx, &rb); err != nil {
			return fmt.Errorf("graph identity: update rolebinding %s: %w", key, err)
		}
		return nil
	})
}

func splitNamespaces(s string) []string {
	var out []string
	for _, ns := range strings.Split(s, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			out = append(out, ns)
		}
	}
	return out
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

// RefNamespaces returns the sorted, de-duplicated namespaces that g's ref
// nodes read from. A ref without a namespace reads from g's namespace.
func RefNamespaces(g *Graph) []string {
	seen := map[string]bool{}
	for _, n := range g.Spec.Nodes {
		if n.Ref == nil {
			continue
		}
		seen[RefNamespace(g, n)] = true
	}
	return sortedKeys(seen)
}

// RefNamespace returns the namespace ref node n of g reads from.
func RefNamespace(g *Graph, n GraphNode) string {
	if md, ok := n.Ref["metadata"].(map[string]interface{}); ok {
		if v, ok := md["namespace"].(string); ok && v != "" {
			return v
		}
	}
	return g.Namespace
}
