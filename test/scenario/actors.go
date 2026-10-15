// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"context"
	"errors"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	airec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
)

// The cluster admin that sets a scenario up (Apply).
const AdminUser = "kubernetes-admin"

// AdminGroups are AdminUser's groups.
var AdminGroups = []string{"system:masters", "system:authenticated"}

type userKey struct{}

// AsUser is ctx with the user its writes are made as.
func AsUser(ctx context.Context, name string, groups ...string) context.Context {
	return AsInfo(ctx, &user.DefaultInfo{Name: name, Groups: groups})
}

// AsInfo is ctx with the user (with groups and extra) its writes are made as.
func AsInfo(ctx context.Context, u user.Info) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

// ServiceAccountGroups are the groups the API server gives a ServiceAccount
// of ns.
func ServiceAccountGroups(ns string) []string {
	return []string{"system:serviceaccounts", "system:serviceaccounts:" + ns, "system:authenticated"}
}

// ControllerInfo is the controller as the API server authenticates it: its
// ServiceAccount's bound token, which carries the credential and Pod extras.
func ControllerInfo() user.Info {
	return &user.DefaultInfo{Name: ControllerUser, Groups: ServiceAccountGroups(ReleaseNamespace),
		Extra: map[string][]string{
			"authentication.kubernetes.io/credential-id": {"JTI=scenario-controller"},
			"authentication.kubernetes.io/pod-name":      {Release + "-0"},
			"authentication.kubernetes.io/pod-uid":       {"uid-controller-pod"},
			"authentication.kubernetes.io/node-name":     {"node-0"},
		}}
}

// GraphInfo is kro applying a Graph's objects in ns: impersonating the
// Graph ServiceAccount, so with its groups and no extra of its own.
func GraphInfo(ns string) user.Info {
	return &user.DefaultInfo{Name: GraphUser(ns), Groups: ServiceAccountGroups(ns)}
}

// gitopsUser is the GitOps engine's controller (Argo CD, Flux).
const gitopsUser = "system:serviceaccount:gitops-system:gitops-controller"

// userOf is the user of ctx: the controller when none is set.
func userOf(ctx context.Context) user.Info {
	if u, ok := ctx.Value(userKey{}).(user.Info); ok {
		return u
	}
	return ControllerInfo()
}

// startActors serves the chart's identity and actor policies and builds the
// agent index, as the controller does at start (ensureIndex).
func (w *World) startActors(maxBytes int) {
	w.T.Helper()
	c, err := RenderChart()
	if err != nil {
		w.T.Fatal(err)
	}
	// Every policy the chart installs.
	names := make([]string, 0, len(c.VAPs))
	for n := range c.VAPs {
		names = append(names, n)
	}
	sort.Strings(names)
	api, stop, err := NewAPIServer(PolicyObjects(nil, c, names, nil), nil)
	if err != nil {
		w.T.Fatal(err)
	}
	w.T.Cleanup(stop)
	w.api = api
	w.identities = &airec.Reconciler{Client: w.Client, MaxIndexBytes: maxBytes}
	w.index = &airec.IndexReconciler{Client: w.Client, Name: Release, MaxIndexBytes: maxBytes}
	if _, err := w.index.Reconcile(w.Ctx, ctrl.Request{}); err != nil {
		w.T.Fatalf("build the agent index: %v", err)
	}
}

// PauseIndex stops (true) or restarts the AgentIndex reconciler: an index
// the controller has not built yet.
func (w *World) PauseIndex(paused bool) {
	w.indexPaused.Store(paused)
}

// reconcileIndex runs the AgentIndex reconciler when a watch would wake it:
// an AgentIdentity or the index changed, or the index is gone (its own watch
// recreates it).
func (w *World) reconcileIndex(k reconcileKind) {
	if w.indexPaused.Load() {
		return
	}
	var cur v1alpha1.AgentIndex
	o := client.Object(&v1alpha1.AgentIndex{})
	o.SetName(Release)
	if err := w.Client.Get(w.Ctx, types.NamespacedName{Name: Release}, &cur); err == nil {
		o = &cur
	}
	w.reconcileObject(k, o)
}

// stored returns the stored copy of o, or nil when there is none.
func (w *World) stored(ctx context.Context, c client.Reader, o client.Object) (client.Object, error) {
	if w.api == nil {
		return nil, nil
	}
	cur, ok := o.DeepCopyObject().(client.Object)
	if !ok {
		return nil, fmt.Errorf("copy %T", o)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(o), cur); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get %s %s: %w", objKind(o), o.GetName(), err)
	}
	return cur, nil
}

// admitWrite admits a write of o (old: the stored object) as the user of
// ctx through the chart's policies; nil when no policy matches its kind.
func (w *World) admitWrite(ctx context.Context, c client.Client, op admission.Operation, o, old client.Object) error {
	return w.admitSub(ctx, c, op, "", o, old)
}

// admitSub is admitWrite for a write of subresource (status) of o.
func (w *World) admitSub(ctx context.Context, c client.Client, op admission.Operation, sub string, o, old client.Object) error {
	if w.api == nil {
		return nil
	}
	src := o
	if src == nil {
		src = old
	}
	gvk, err := c.GroupVersionKindFor(src)
	if err != nil {
		return nil //nolint:nilerr // not a kind the policies match
	}
	m, err := w.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil || !w.api.Matches(gvk.Group, m.Resource.Resource, sub) {
		return nil //nolint:nilerr // not a kind the policies match
	}
	if w.indexDirty.Swap(false) {
		if err := w.syncIndex(ctx, c); err != nil {
			w.indexDirty.Store(true)
			return err
		}
	}
	if err := w.api.EnsureNamespace(src.GetNamespace()); err != nil {
		return err
	}
	toMap := func(x client.Object) (map[string]interface{}, error) {
		if x == nil {
			return nil, nil
		}
		u, err := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(x)
		if err != nil {
			return nil, fmt.Errorf("admit %s: %w", gvk.Kind, err)
		}
		u["apiVersion"], u["kind"] = gvk.GroupVersion().String(), gvk.Kind
		if err := w.Admit.Default(gvk.Kind, u); err != nil {
			return nil, err
		}
		return u, nil
	}
	newM, err := toMap(o)
	if err != nil {
		return err
	}
	oldM, err := toMap(old)
	if err != nil {
		return err
	}
	return w.api.AdmitSub(op, m.Resource, sub, newM, oldM, userOf(ctx))
}

// admitPatched admits a patch that is already stored (o: the result, old:
// the object before), and puts old back when the policies refuse it.
func (w *World) admitPatched(ctx context.Context, c client.WithWatch, sub string, o, old client.Object) error {
	if w.api == nil || old == nil {
		return nil
	}
	err := w.admitSub(ctx, c, admission.Update, sub, o, old)
	if err == nil {
		return nil
	}
	old.SetResourceVersion(o.GetResourceVersion())
	var rerr error
	if sub != "" {
		rerr = c.SubResource(sub).Update(ctx, old)
	} else {
		rerr = c.Update(ctx, old)
	}
	if rerr != nil {
		return fmt.Errorf("restore %s %s after a refused patch: %w", objKind(o), o.GetName(), rerr)
	}
	return err
}

// syncIndex serves the stored AgentIndex to the admission plugin.
func (w *World) syncIndex(ctx context.Context, c client.Reader) error {
	var idx v1alpha1.AgentIndex
	if err := c.Get(ctx, types.NamespacedName{Name: Release}, &idx); err != nil {
		if apierrors.IsNotFound(err) {
			return w.api.SetAgentIndex(nil)
		}
		return fmt.Errorf("get agent index: %w", err)
	}
	return w.api.SetAgentIndex(&idx)
}

// Identity is an authenticated user, as the API server's SelfSubjectReview
// returns it.
type Identity struct {
	Username string
	Groups   []string
	// Asserted is what the caller says about itself (KARDINAL_ACTOR_*).
	Asserted v1alpha1.AssertedActor
}

// CtxOf is w.Ctx as id.
func (w *World) CtxOf(id Identity) context.Context {
	return AsUser(w.Ctx, id.Username, id.Groups...)
}

// StampFor is the actor stamp kardinal's CLI writes for id at where
// (cmd/kardinal/cmd/identity.go stampOf): id through the cluster's agent
// index with the asserted fields. It fails as the CLI does when id is an
// agent that may not act there, before anything is written; with no ready
// index it is nil and the actor policy decides.
func (w *World) StampFor(id Identity, where actor.Where) (*v1alpha1.Actor, error) {
	src := &actor.Source{Reader: w.Client}
	r, err := src.Resolve(w.Ctx, actor.User{Username: id.Username, Groups: id.Groups}, where)
	switch {
	case errors.Is(err, actor.ErrConflict), errors.Is(err, actor.ErrBlocked), errors.Is(err, actor.ErrOutOfScope):
		return nil, err
	case err != nil:
		return nil, nil //nolint:nilerr // the CLI writes no stamp and warns
	}
	a := id.Asserted
	return actor.Stamp(r, &a), nil
}

// CreateBundle creates b as id the way kardinal create bundle does: the
// creator annotation and the actor stamp (StampFor). cliErr is the CLI's own
// refusal (nothing written); refused are the API server's reasons.
func (w *World) CreateBundle(id Identity, b *v1alpha1.Bundle) (cliErr error, refused []string, err error) {
	lifecycle.StampCreatedBy(b, id.Username)
	stamp, cliErr := w.StampFor(id, actor.Where{Namespace: b.Namespace, Pipeline: b.Spec.Pipeline})
	if cliErr != nil {
		return cliErr, nil, nil
	}
	b.Spec.Actor = stamp
	refused, err = w.ApplyAs(w.CtxOf(id), b)
	return nil, refused, err
}

// AdmitCreate runs the creation of obj as the user of ctx through the
// chart's policies only, writing nothing: nil when admitted.
func (w *World) AdmitCreate(ctx context.Context, obj client.Object) error {
	return w.admitWrite(ctx, w.store, admission.Create, obj, nil)
}

// AdmitUpdate runs the update of old to obj (of its subresource sub, when
// not empty) as the user of ctx through the chart's policies only, writing
// nothing.
func (w *World) AdmitUpdate(ctx context.Context, sub string, obj, old client.Object) error {
	return w.admitSub(ctx, w.store, admission.Update, sub, obj, old)
}

// Policies are the ValidatingAdmissionPolicies the World serves.
func (w *World) Policies() []string {
	if w.api == nil {
		return nil
	}
	return w.api.policies
}

// Store is the World's object store itself: reads and writes through it
// skip admission, as a write straight to etcd would.
func (w *World) Store() client.Client {
	return w.store
}
