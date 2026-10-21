//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// TestActorIndexNotReadyOnAPIServer (#2451): on a real kube-apiserver with
// the chart's policies, in each state the AgentIndex is not Ready (never
// built: no status; IndexFull; a later release's CompactFormat), the index's
// readiness never refuses a person: their version, freeze, freeze exemption
// and CLI pause are admitted, stamped unverified as kardinal writes them,
// and refused only stamped human (it cannot be verified). A ServiceAccount's
// version, freeze exemption and stamped pause are refused, with the state's
// message.
// Covers ACTOR-INDEX-06.
func TestActorIndexNotReadyOnAPIServer(t *testing.T) {
	require.NotEmpty(t, os.Getenv("KUBEBUILDER_ASSETS"))
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(repoRoot(t), "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	require.NoError(t, v1alpha1.AddToScheme(sch))
	admin, err := client.New(cfg, client.Options{Scheme: sch})
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}}))
	require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "all-admin"},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects: []rbacv1.Subject{{Kind: "Group", APIGroup: rbacv1.GroupName, Name: "system:authenticated"}}}))
	for _, d := range helmInstallOrder(render(t, "kardinal-promoter")) {
		if d.Kind != "AgentIndex" && d.Kind != "ValidatingAdmissionPolicy" && d.Kind != "ValidatingAdmissionPolicyBinding" {
			continue
		}
		var obj unstructured.Unstructured
		require.NoError(t, json.Unmarshal(d.raw, &obj.Object))
		require.NoError(t, admin.Create(ctx, &obj), d.Kind+"/"+d.Name)
	}
	as := func(user string) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: user}
		cl, err := client.New(c, client.Options{Scheme: sch})
		require.NoError(t, err)
		return cl
	}
	controller := asController(t, cfg, sch)
	const ci = "system:serviceaccount:team-a:ci"
	unverified := func(p string) *v1alpha1.Actor { return &v1alpha1.Actor{Kind: v1alpha1.ActorUnverified, Principal: p} }
	n := 0
	bundle := func(a *v1alpha1.Actor) *v1alpha1.Bundle {
		n++
		return &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("b%d", n), Namespace: "team-a"},
			Spec: v1alpha1.BundleSpec{Pipeline: "checkout", Type: "image", Actor: a}}
	}
	app := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "team-a"},
		Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: "https://github.com/acme/app"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}}}}
	require.NoError(t, admin.Create(ctx, app))
	pause := func(c client.Client, by string, a *v1alpha1.Actor) error {
		var cur v1alpha1.Pipeline
		require.NoError(t, admin.Get(ctx, client.ObjectKeyFromObject(app), &cur))
		cur.Spec.Paused = !cur.Spec.Paused
		action := "Pause"
		if !cur.Spec.Paused {
			action = "Resume"
		}
		cur.Spec.LastChange = &v1alpha1.PipelineChange{Action: action, By: by, Actor: a}
		cur.Labels = map[string]string{"kardinal.io/actor-check": "true"} // as kardinal pause writes it (#1936)
		return c.Update(ctx, &cur)
	}
	setStatus := func(st v1alpha1.AgentIndexStatus) {
		var idx v1alpha1.AgentIndex
		require.NoError(t, admin.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx))
		idx.Status = st
		require.NoError(t, controller.Status().Update(ctx, &idx), "only the controller writes the index status")
	}
	for i, state := range []struct {
		name   string
		status *v1alpha1.AgentIndexStatus // nil: as the chart creates it, no status
		why    string
	}{
		{"never built", nil, "agent index is not ready yet"},
		{"IndexFull", &v1alpha1.AgentIndexStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse,
			Reason: "IndexFull", Message: "full", LastTransitionTime: metav1.Now()}}}, "agent index is full (IndexFull"},
		{"CompactFormat", &v1alpha1.AgentIndexStatus{
			CompactUsers: map[string]string{ci: "fixer\x7falice\x7fauto\x7fteam-*\x7f*\x7f*\x7f"},
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: actor.ReasonCompactFormat,
				Message: "compact", LastTransitionTime: metav1.Now()},
				{Type: actor.ConditionCompactReady, Status: metav1.ConditionTrue, Reason: "Built", Message: "built",
					LastTransitionTime: metav1.Now()}}}, "agent index is in the compact format of a newer release"},
	} {
		if state.status != nil {
			setStatus(*state.status)
		}
		// The policies see the state: a ServiceAccount's version is refused
		// with its message (the param informer is eventually consistent).
		require.Eventually(t, func() bool {
			err := as(ci).Create(ctx, bundle(nil))
			return err != nil && strings.Contains(err.Error(), state.why)
		}, 30*time.Second, 500*time.Millisecond, "%s: a ServiceAccount's version is refused", state.name)

		require.NoError(t, as("alice").Create(ctx, bundle(unverified("alice"))), "%s: a person's version", state.name)
		require.NoError(t, as("alice").Create(ctx, bundle(nil)), "%s: a person's unstamped version", state.name)
		err := as("alice").Create(ctx, bundle(&v1alpha1.Actor{Kind: "human", Principal: "alice"}))
		require.Error(t, err, "%s: a person's version stamped human", state.name)
		require.Contains(t, err.Error(), "actor must be {kind: unverified, principal: alice}")

		fz := &v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("kardinal-freeze-everyone-%d", i)},
			Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Start: metav1.Now(), Reason: "INC-1",
				Freeze: &v1alpha1.ChangeWindowFreeze{Actors: "everyone", CreatedBy: "alice", Actor: unverified("alice")}}}
		require.NoError(t, as("alice").Create(ctx, fz), "%s: a person freezes", state.name)
		exempt := func(c client.Client, by string, a *v1alpha1.Actor) error {
			var cur v1alpha1.ChangeWindow
			require.NoError(t, admin.Get(ctx, client.ObjectKeyFromObject(fz), &cur))
			cur.Spec.Freeze.Exemptions = append(cur.Spec.Freeze.Exemptions, v1alpha1.FreezeExemption{Namespace: "team-a",
				Pipeline: "checkout", Reason: "hotfix", CreatedBy: by, ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)), Actor: a})
			return c.Update(ctx, &cur)
		}
		require.NoError(t, exempt(as("bob"), "bob", unverified("bob")), "%s: a person's freeze exemption", state.name)
		err = exempt(as(ci), ci, nil)
		require.Error(t, err, "%s: a ServiceAccount's freeze exemption", state.name)
		require.Contains(t, err.Error(), state.why)

		require.NoError(t, pause(as("alice"), "alice", unverified("alice")), "%s: a person's CLI pause", state.name)
		err = pause(as(ci), ci, &v1alpha1.Actor{Kind: "bot", Principal: ci})
		require.Error(t, err, "%s: a ServiceAccount's stamped pause", state.name)
		require.Contains(t, err.Error(), state.why)
		t.Logf("ok %s", state.name)
	}
}
