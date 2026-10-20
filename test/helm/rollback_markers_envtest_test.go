//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestRollbackMarkersOnAPIServer (#2225): on a real API server, with the
// chart's bundle-creator policy, the controller's rollback Bundle keeps its
// markers. Someone with full rights on Bundles cannot relink its
// kardinal.io/rollback-from, or change or remove its kardinal.io/rollback
// and kardinal.io/pipeline labels, by update, merge patch or JSON patch,
// while other labels stay editable and the controller can still change
// them. Its spec.provenance.rollbackFrom, which the controller binds the
// rollback on, cannot change at all (the CRD's provenance rule). CI runs it
// on the oldest and newest supported API servers.
//
// Covers RB-AUTO-FORGED-13.
func TestRollbackMarkersOnAPIServer(t *testing.T) {
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
	for _, ns := range []string{releaseNS, "payments"} {
		require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	}
	installed := 0
	for _, d := range render(t, "kardinal-promoter") {
		if (d.Kind != "ValidatingAdmissionPolicy" && d.Kind != "ValidatingAdmissionPolicyBinding") || !strings.Contains(d.Name, "bundle-creator") {
			continue
		}
		var obj unstructured.Unstructured
		require.NoError(t, json.Unmarshal(d.raw, &obj.Object))
		require.NoError(t, admin.Create(ctx, &obj), d.Kind+"/"+d.Name)
		installed++
	}
	require.GreaterOrEqual(t, installed, 2, "the bundle-creator policy and its binding")
	controllerSA := "system:serviceaccount:" + releaseNS + ":kardinal-promoter"
	require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "everyone"},
		RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects: []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: "mallory"},
			{Kind: "ServiceAccount", Name: "kardinal-promoter", Namespace: releaseNS}}}))
	as := func(user string) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: user}
		cl, err := client.New(c, client.Options{Scheme: sch})
		require.NoError(t, err)
		return cl
	}
	ctrl, mallory := as(controllerSA), as("mallory")

	rb := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v2-rollback-alarm", Namespace: "payments",
		Labels:      map[string]string{"kardinal.io/rollback": "true", "kardinal.io/pipeline": "app", "team": "a"},
		Annotations: map[string]string{"kardinal.io/rollback-from": "app-v2", "kardinal.io/created-by": "kardinal-controller"}},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "app",
			Images:     []v1alpha1.ImageRef{{Repository: "ghcr.io/acme/app", Tag: "1.0.0"}},
			Intent:     &v1alpha1.BundleIntent{TargetEnvironment: "prod"},
			Provenance: &v1alpha1.BundleProvenance{RollbackOf: "app-v1", RollbackFrom: "app-v2"},
			Actor:      &v1alpha1.Actor{Kind: v1alpha1.ActorController, Principal: controllerSA, Via: v1alpha1.ViaAutoRollback}}}
	require.NoError(t, ctrl.Create(ctx, rb))
	key := client.ObjectKeyFromObject(rb)

	// The policy may take a moment to apply after it is created.
	refused := func(name string, write func() error) {
		t.Helper()
		var err error
		deadline := time.Now().Add(30 * time.Second)
		for {
			if err = write(); err != nil || time.Now().After(deadline) {
				break
			}
			// Admitted before the policy applied: put the marker back.
			var cur v1alpha1.Bundle
			require.NoError(t, admin.Get(ctx, key, &cur))
			cur.Labels = map[string]string{"kardinal.io/rollback": "true", "kardinal.io/pipeline": "app", "team": "a"}
			cur.Annotations["kardinal.io/rollback-from"] = "app-v2"
			require.NoError(t, ctrl.Update(ctx, &cur))
			time.Sleep(time.Second)
		}
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "cannot be added, changed or removed on a Bundle the controller created", name)
	}
	patch := func(c client.Client, kind types.PatchType, body string) error {
		return c.Patch(ctx, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}, client.RawPatch(kind, []byte(body)))
	}
	refused("relink rollback-from by merge patch", func() error {
		return patch(mallory, types.MergePatchType, `{"metadata":{"annotations":{"kardinal.io/rollback-from":"app-v3"}}}`)
	})
	refused("remove rollback-from by JSON patch", func() error {
		return patch(mallory, types.JSONPatchType, `[{"op":"remove","path":"/metadata/annotations/kardinal.io~1rollback-from"}]`)
	})
	refused("remove the rollback label", func() error {
		return patch(mallory, types.JSONPatchType, `[{"op":"remove","path":"/metadata/labels/kardinal.io~1rollback"}]`)
	})
	refused("change the pipeline label by update", func() error {
		var cur v1alpha1.Bundle
		require.NoError(t, mallory.Get(ctx, key, &cur))
		cur.Labels["kardinal.io/pipeline"] = "other"
		return mallory.Update(ctx, &cur)
	})
	require.NoError(t, patch(mallory, types.MergePatchType, `{"metadata":{"labels":{"team":"b"},"annotations":{"note":"x"}}}`),
		"other labels and annotations stay editable")
	require.NoError(t, patch(ctrl, types.MergePatchType, `{"metadata":{"annotations":{"kardinal.io/rollback-from":"app-v2b"}}}`),
		"the controller may")
	err = patch(admin, types.MergePatchType, `{"spec":{"provenance":{"rollbackFrom":"app-v3"}}}`)
	require.Error(t, err, "spec.provenance.rollbackFrom cannot change")
	assert.Contains(t, err.Error(), "spec.provenance is immutable")

	var final v1alpha1.Bundle
	require.NoError(t, admin.Get(ctx, key, &final))
	assert.Equal(t, "app-v2b", final.Annotations["kardinal.io/rollback-from"])
	assert.Equal(t, "app-v2", final.Spec.Provenance.RollbackFrom)
	assert.Equal(t, map[string]string{"kardinal.io/rollback": "true", "kardinal.io/pipeline": "app", "team": "b"}, final.Labels)

	// A Bundle a person created keeps editable markers.
	own := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v9", Namespace: "payments",
		Labels: map[string]string{"kardinal.io/rollback": "true", "kardinal.io/pipeline": "app"}},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "app", Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/acme/app", Tag: "9"}}}}
	require.NoError(t, mallory.Create(ctx, own))
	require.NoError(t, mallory.Patch(ctx, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v9", Namespace: "payments"}},
		client.RawPatch(types.JSONPatchType, []byte(`[{"op":"remove","path":"/metadata/labels/kardinal.io~1rollback"}]`))))
}
