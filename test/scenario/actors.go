// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"context"
	"errors"
	"fmt"

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

// The chart's policies a World with Actors serves: who a record names
// (identity-admission.yaml) and the actor stamp (actor-admission.yaml).
var actorPolicies = []string{"actor", "audit-actor", "agent-identities", "approvals", "bundle-rejection", "bundle-creator",
	"gate-overrides"}

type userKey struct{}

// AsUser is ctx with the user its writes are made as.
func AsUser(ctx context.Context, name string, groups ...string) context.Context {
	return context.WithValue(ctx, userKey{}, &user.DefaultInfo{Name: name, Groups: groups})
}

// userOf is the user of ctx: the controller's ServiceAccount when none is set.
func userOf(ctx context.Context) user.Info {
	if u, ok := ctx.Value(userKey{}).(user.Info); ok {
		return u
	}
	return &user.DefaultInfo{Name: ControllerUser, Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + ReleaseNamespace,
		"system:authenticated"}}
}

// startActors serves the chart's identity and actor policies and builds the
// agent index, as the controller does at start (ensureIndex).
func (w *World) startActors(maxBytes int) {
	w.T.Helper()
	c, err := RenderChart()
	if err != nil {
		w.T.Fatal(err)
	}
	names := make([]string, 0, len(actorPolicies))
	for _, p := range actorPolicies {
		names = append(names, Release+"-"+p)
	}
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
	if w.api == nil {
		return nil
	}
	src := o
	if src == nil {
		src = old
	}
	gvk, err := c.GroupVersionKindFor(src)
	if err != nil || gvk.Group != "kardinal.io" {
		return nil //nolint:nilerr // not a kind the policies match
	}
	m, err := w.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil || !w.api.Matches(m.Resource.Resource) {
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
	return w.api.Admit(op, m.Resource, newM, oldM, userOf(ctx))
}

// admitPatched admits a patch that is already stored (o: the result, old:
// the object before), and puts old back when the policies refuse it.
func (w *World) admitPatched(ctx context.Context, c client.WithWatch, o, old client.Object) error {
	if w.api == nil || old == nil {
		return nil
	}
	err := w.admitWrite(ctx, c, admission.Update, o, old)
	if err == nil {
		return nil
	}
	old.SetResourceVersion(o.GetResourceVersion())
	if rerr := c.Update(ctx, old); rerr != nil {
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
