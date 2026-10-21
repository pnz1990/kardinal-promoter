// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

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

// startAPIServer serves the chart's identity and actor policies through
// the API server's ValidatingAdmissionPolicy plugin. The API server
// outlives a controller restart (Restart).
func (w *World) startAPIServer(budgets bool) {
	w.T.Helper()
	values := map[string]any{}
	if budgets {
		values["actors"] = map[string]any{"budgets": map[string]any{"enabled": true}}
	}
	c, err := RenderChartWith(values)
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
}

// startRegistry starts the agent registry's reconciler and builds the
// index, as the controller does at start.
func (w *World) startRegistry(maxBytes int) {
	w.T.Helper()
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
	u := userOf(ctx)
	start := time.Now()
	err = w.api.AdmitSub(op, m.Resource, sub, newM, oldM, u)
	w.recordAdmission(gvk.Kind, op, sub, u.GetName(), time.Since(start))
	return err
}

// RecordAdmissions starts (true) or stops recording how long each write
// the chart's policies match takes the API server's admission plugin
// (AdmissionLatencies).
func (w *World) RecordAdmissions(on bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if on {
		w.admissions = map[string][]time.Duration{}
	} else {
		w.admissions = nil
	}
}

// AdmissionLatencies are the admission times recorded since
// RecordAdmissions(true), by "<Kind> <OPERATION>[/<subresource>] by
// <who>", who being the controller, kro, a ServiceAccount or a user.
func (w *World) AdmissionLatencies() map[string][]time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string][]time.Duration, len(w.admissions))
	for k, v := range w.admissions {
		out[k] = append([]time.Duration(nil), v...)
	}
	return out
}

func (w *World) recordAdmission(kind string, op admission.Operation, sub, username string, d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.admissions == nil {
		return
	}
	who := "user"
	switch {
	case username == ControllerUser:
		who = "controller"
	case strings.HasPrefix(username, "system:serviceaccount:") && strings.HasSuffix(username, ":"+graphSA):
		who = "kro"
	case strings.HasPrefix(username, "system:serviceaccount:"):
		who = "serviceaccount"
	}
	key := kind + " " + string(op)
	if sub != "" {
		key += "/" + sub
	}
	key += " by " + who
	w.admissions[key] = append(w.admissions[key], d)
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
	if cur, gerr := w.stored(ctx, c, o); gerr == nil && cur != nil {
		old.SetResourceVersion(cur.GetResourceVersion())
	}
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

// WithEmptyAgentIndex runs fn with the admission plugin serving the stored
// AgentIndex with no entries (Ready, as the controller wrote it): a
// calibration of what a write's admission costs without the index. The
// stored index is served again after it.
func (w *World) WithEmptyAgentIndex(fn func()) {
	w.T.Helper()
	if w.api == nil {
		w.T.Fatal("WithEmptyAgentIndex: no admission plugin (Options.Actors, without NoAdmission)")
	}
	var idx v1alpha1.AgentIndex
	if err := w.store.Get(w.Ctx, types.NamespacedName{Name: Release}, &idx); err != nil {
		w.T.Fatalf("get agent index: %v", err)
	}
	idx.Status.Users, idx.Status.Groups = map[string]v1alpha1.AgentIndexEntry{}, map[string]v1alpha1.AgentIndexEntry{}
	if err := w.api.SetAgentIndex(&idx); err != nil {
		w.T.Fatal(err)
	}
	defer w.indexDirty.Store(true)
	fn()
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
