// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package graphcleanup finishes what kro cannot when kardinal's Graphs go
// away: it lets a Graph in a namespace being deleted go once kro can no longer
// tear it down, and it deletes the reader RoleBindings no Graph needs any
// more.
package graphcleanup

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

// KroFinalizer is the finalizer kro keeps on a Graph until it has deleted the
// Graph's resources (kro pkg/controller/graph, setUnmanaged).
const KroFinalizer = "kro.run/graph-finalizer"

// requeueApplierBinding is how often a deleting Graph in a terminating
// namespace is checked again while its applier RoleBinding still exists, so
// kro can still tear it down itself.
const requeueApplierBinding = 10 * time.Second

// requeueConflict is how soon kro's finalizer is removed again when the Graph
// changed under every retry.
const requeueConflict = time.Second

// requeueNamespace is how often a deleting Graph whose namespace is not being
// deleted is checked again. The controller only gets namespaces, so it sees
// no event when one starts terminating; a Graph whose teardown was already
// stuck then (its applier RoleBinding was deleted first, so kro's deletes are
// forbidden and kro writes nothing) is found by this requeue instead.
const requeueNamespace = 30 * time.Second

// GraphLister lists the kardinal Graphs in a namespace (graph.GraphClient).
type GraphLister interface {
	List(ctx context.Context, namespace string) ([]*graph.Graph, error)
}

// Reconciler watches kardinal's Graphs.
//
// When a Graph is gone it prunes the reader RoleBindings of its namespace
// that no remaining Graph reads through (IdentityProvisioner.Prune). Before,
// only a Bundle translation pruned, so the bindings of a namespace's last
// Bundle stayed for good, granting its Graph ServiceAccount read access in
// argocd or flux-system.
//
// When a Graph is deleted in a namespace being deleted it may also remove
// kro's finalizer. kro tears a Graph down as the Graph ServiceAccount, which
// the applier RoleBinding authorizes; the namespace controller deletes that
// RoleBinding along with everything else, in no set order. Once it is gone,
// every delete kro makes is forbidden, kro keeps its finalizer, and the
// namespace stays Terminating for good. The Reconciler removes the finalizer
// only when all of these hold:
//
//   - the Graph is kardinal's (the kardinal.io/bundle label and a controller
//     owner reference to a kardinal.io Bundle) and has a deletionTimestamp;
//   - its namespace is Terminating (or gone);
//   - the applier RoleBinding is gone or being deleted.
//
// A deleting Graph is checked again every requeueNamespace while its
// namespace is not terminating: there is no namespace watch, and a Graph
// stuck before its namespace was deleted gets no other event.
//
// kardinal's Graphs create their resources (PromotionSteps, PolicyGates,
// PRStatuses) in the Graph's own namespace, which the namespace controller
// empties. A resource kro recorded in another namespace, or a cluster-scoped
// one, is deleted with the controller's client first; while that fails the
// finalizer stays.
type Reconciler struct {
	// Client reads Graphs and patches their finalizers, and deletes Graph
	// resources outside the Graph's namespace.
	Client client.Client
	// APIReader reads Namespaces and RoleBindings from the API server, so the
	// controller needs neither a cache nor list/watch on them.
	APIReader client.Reader
	// Graphs lists the Graphs Prune must keep bindings for.
	Graphs GraphLister
	// Identity provisions the Graph RoleBindings. Nil (identity managed outside
	// the controller) turns the Reconciler off.
	Identity *graph.IdentityProvisioner
}

// Reconcile handles one Graph event.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.Identity == nil {
		return ctrl.Result{}, nil
	}
	log := zerolog.Ctx(ctx).With().Str("graph", req.Name).Str("namespace", req.Namespace).Logger()

	if req.Name == pruneRequest {
		return ctrl.Result{}, r.prune(ctx, log, req.Namespace)
	}

	g := &unstructured.Unstructured{}
	g.SetGroupVersionKind(graph.GraphGVK)
	if err := r.Client.Get(ctx, req.NamespacedName, g); err != nil {
		if apierrors.IsNotFound(err) {
			// Gone: its delete event queued the namespace's prune
			// (pruneOnDelete). A delete missed during a restart is pruned
			// by the startup sweep, which runs only when --watch-namespace
			// is empty, on the default shard (cmd/kardinal-controller); in
			// namespace mode the next prune of the namespace covers it.
			// Pruning here as well ran one uncached prune per deleted Graph
			// again.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get graph %s: %w", req, err)
	}
	if !IsKardinalGraph(g) || g.GetDeletionTimestamp() == nil || !controllerutil.ContainsFinalizer(g, KroFinalizer) {
		return ctrl.Result{}, nil
	}

	terminating, err := r.namespaceTerminating(ctx, req.Namespace)
	switch {
	case err != nil:
		return ctrl.Result{}, err
	case !terminating:
		// kro tears the Graph down; look again in case the namespace starts
		// terminating while the teardown is stuck.
		return ctrl.Result{RequeueAfter: requeueNamespace}, nil
	}
	var rb rbacv1.RoleBinding
	err = r.APIReader.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: r.Identity.ApplierBindingName()}, &rb)
	switch {
	case err == nil && rb.DeletionTimestamp == nil:
		// kro can still delete the Graph's resources itself.
		return ctrl.Result{RequeueAfter: requeueApplierBinding}, nil
	case err != nil && !apierrors.IsNotFound(err):
		return ctrl.Result{}, fmt.Errorf("get rolebinding %s/%s: %w", req.Namespace, r.Identity.ApplierBindingName(), err)
	}

	if err := r.deleteOutside(ctx, log, g); err != nil {
		return ctrl.Result{}, err
	}
	removed, err := r.removeKroFinalizer(ctx, g)
	switch {
	case apierrors.IsConflict(err):
		// The Graph kept changing under every retry: try again shortly. A
		// conflict is expected while kro and the namespace controller write
		// the Graph too, so it is not a reconcile error.
		log.Debug().Err(err).Msg("graph changed while removing kro's finalizer; trying again")
		return ctrl.Result{RequeueAfter: requeueConflict}, nil
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("remove finalizer %s from graph %s: %w", KroFinalizer, req, err)
	case !removed:
		return ctrl.Result{}, nil
	}
	// The namespace is terminating, so no Event can be created in it.
	log.Info().Str("finalizer", KroFinalizer).
		Msg("removed kro's finalizer from a Graph in a terminating namespace: its applier RoleBinding " +
			"is gone, so kro cannot delete the Graph's resources; the namespace deletion deletes them")
	return ctrl.Result{}, nil
}

// removeKroFinalizer removes kro's finalizer from g. The patch carries g's
// resourceVersion, so it fails with a conflict when kro or the namespace
// controller wrote the Graph since it was read. It then reads the Graph again
// from the API server (the cache can still serve the old one) and retries
// against that copy. It reports false when the Graph is gone or no longer has
// the finalizer, and returns the conflict when every retry hit one.
func (r *Reconciler) removeKroFinalizer(ctx context.Context, g *unstructured.Unstructured) (bool, error) {
	key := client.ObjectKeyFromObject(g)
	removed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		base := g.DeepCopy()
		controllerutil.RemoveFinalizer(g, KroFinalizer)
		err := r.Client.Patch(ctx, g, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		if err == nil {
			removed = true
			return nil
		}
		if !apierrors.IsConflict(err) {
			return client.IgnoreNotFound(err)
		}
		fresh := &unstructured.Unstructured{}
		fresh.SetGroupVersionKind(graph.GraphGVK)
		if gerr := r.APIReader.Get(ctx, key, fresh); gerr != nil {
			if apierrors.IsNotFound(gerr) {
				return nil
			}
			return fmt.Errorf("get graph %s: %w", key, gerr)
		}
		if !controllerutil.ContainsFinalizer(fresh, KroFinalizer) {
			return nil
		}
		g = fresh
		return err
	})
	return removed, err
}

// prune deletes the reader RoleBindings of namespace that no remaining Graph
// reads through.
func (r *Reconciler) prune(ctx context.Context, log zerolog.Logger, namespace string) error {
	defer r.Identity.LockNamespace(namespace)()
	graphs, err := r.Graphs.List(ctx, namespace)
	if err != nil {
		return fmt.Errorf("list graphs for prune: %w", err)
	}
	if err := r.Identity.Prune(ctx, namespace, graphs); err != nil {
		return fmt.Errorf("prune reader rolebindings: %w", err)
	}
	log.Debug().Int("graphs", len(graphs)).Msg("graph identity: pruned reader rolebindings after a Graph delete")
	return nil
}

// namespaceTerminating reports whether namespace is being deleted. A
// namespace that is already gone counts.
func (r *Reconciler) namespaceTerminating(ctx context.Context, namespace string) (bool, error) {
	var ns corev1.Namespace
	if err := r.APIReader.Get(ctx, types.NamespacedName{Name: namespace}, &ns); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("get namespace %s: %w", namespace, err)
	}
	return ns.DeletionTimestamp != nil || ns.Status.Phase == corev1.NamespaceTerminating, nil
}

// deleteOutside deletes the resources in g's status.managedResources that are
// not in g's namespace: the namespace deletion does not reach them. Each
// delete has a UID precondition, as kro's own, so an object recreated under
// the same name is left alone.
func (r *Reconciler) deleteOutside(ctx context.Context, log zerolog.Logger, g *unstructured.Unstructured) error {
	managed, _, err := unstructured.NestedSlice(g.Object, "status", "managedResources")
	if err != nil {
		return fmt.Errorf("read graph %s/%s status.managedResources: %w", g.GetNamespace(), g.GetName(), err)
	}
	for _, m := range managed {
		res, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		str := func(k string) string { s, _ := res[k].(string); return s }
		ns, uid := str("namespace"), str("uid")
		if ns == g.GetNamespace() || uid == "" {
			continue
		}
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion(str("apiVersion"))
		obj.SetKind(str("kind"))
		obj.SetNamespace(ns)
		obj.SetName(str("name"))
		u := types.UID(uid)
		err := r.Client.Delete(ctx, obj, client.Preconditions{UID: &u}, client.PropagationPolicy(metav1.DeletePropagationBackground))
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return fmt.Errorf("delete %s %s/%s of graph %s/%s: %w",
				obj.GetKind(), ns, obj.GetName(), g.GetNamespace(), g.GetName(), err)
		}
		log.Info().Str("kind", obj.GetKind()).Str("resourceNamespace", ns).Str("name", obj.GetName()).
			Msg("deleted a Graph resource outside the Graph's terminating namespace")
	}
	return nil
}

// IsKardinalGraph reports whether g is a Graph kardinal created: it carries
// the kardinal.io/bundle label and is controlled by a kardinal.io Bundle.
func IsKardinalGraph(g client.Object) bool {
	if g.GetLabels()["kardinal.io/bundle"] == "" {
		return false
	}
	owner := metav1.GetControllerOf(g)
	return owner != nil && owner.Kind == "Bundle" && owner.APIVersion == v1alpha1.GroupVersion.String()
}

// pruneRequest is the request name of a namespace's prune. It is not a
// valid object name, so no Graph has it.
const pruneRequest = "~prune"

// PruneRequest is the request that prunes namespace's reader RoleBindings.
func PruneRequest(namespace string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: pruneRequest}}
}

// pruneOnDelete maps a deleted kardinal Graph to its namespace's prune. The
// work queue holds a key once, so the Graphs deleted in a namespace while a
// prune waits share it. One prune per deleted Graph, each listing every
// Graph of the namespace from the API server, fell behind Graph retirement
// at two Bundles a second (graphcleanup queue at 676).
func pruneOnDelete(_ context.Context, o client.Object) []reconcile.Request {
	return []reconcile.Request{PruneRequest(o.GetNamespace())}
}

// SetupWithManager registers the Reconciler for kro Graphs. It passes
// kardinal's Graphs only: the ones being deleted, including those found at
// startup, by name, and the deleted ones as their namespace's prune.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	g := &unstructured.Unstructured{}
	g.SetGroupVersionKind(graph.GraphGVK)
	deleting := func(o client.Object) bool { return IsKardinalGraph(o) && o.GetDeletionTimestamp() != nil }
	b := ctrl.NewControllerManagedBy(mgr).
		Named("graphcleanup").
		For(g, builder.WithPredicates(predicate.Funcs{
			CreateFunc:  func(e event.CreateEvent) bool { return deleting(e.Object) },
			UpdateFunc:  func(e event.UpdateEvent) bool { return deleting(e.ObjectNew) },
			DeleteFunc:  func(event.DeleteEvent) bool { return false },
			GenericFunc: func(event.GenericEvent) bool { return false },
		})).
		Watches(graphObject(), handler.EnqueueRequestsFromMapFunc(pruneOnDelete),
			builder.WithPredicates(predicate.Funcs{
				CreateFunc:  func(event.CreateEvent) bool { return false },
				UpdateFunc:  func(event.UpdateEvent) bool { return false },
				DeleteFunc:  func(e event.DeleteEvent) bool { return IsKardinalGraph(e.Object) },
				GenericFunc: func(event.GenericEvent) bool { return false },
			}))
	return shard.Active().Complete(b, tracing.WrapReconciler("graphcleanup", r), graphList())
}

// graphObject is an empty kro Graph for a watch.
func graphObject() *unstructured.Unstructured {
	g := &unstructured.Unstructured{}
	g.SetGroupVersionKind(graph.GraphGVK)
	return g
}

// graphList is an empty list of kro Graphs, for the shard gate's re-enqueue.
func graphList() *unstructured.UnstructuredList {
	l := &unstructured.UnstructuredList{}
	gvk := graph.GraphGVK
	gvk.Kind += "List"
	l.SetGroupVersionKind(gvk)
	return l
}
