//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// boundedFields are the free-text fields v0.12 bounded (#1867), by CRD file:
// the property path under spec, and the bound.
var boundedFields = map[string][][]string{
	"kardinal.io_bundles.yaml":       {{"provenance", "author"}, {"provenance", "ciRunURL"}},
	"kardinal.io_policygates.yaml":   {{"overrides", "[]", "reason"}, {"overrides", "[]", "stage"}},
	"kardinal.io_changewindows.yaml": {{"reason"}},
}

// loadCRD reads a CRD of config/crd/bases; unbounded drops the maxLength
// (and the rules) of its boundedFields: the CRD before v0.12.
func loadCRD(t *testing.T, file string, unbounded bool) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "config", "crd", "bases", file))
	require.NoError(t, err)
	if unbounded {
		var doc map[string]interface{}
		require.NoError(t, yaml.Unmarshal(raw, &doc))
		versions, _, _ := unstructured.NestedSlice(doc, "spec", "versions")
		for i := range versions {
			v := versions[i].(map[string]interface{})
			for _, path := range boundedFields[file] {
				fields := []string{"schema", "openAPIV3Schema", "properties", "spec"}
				for _, step := range path {
					if step == "[]" {
						fields = append(fields, "items")
					} else {
						fields = append(fields, "properties", step)
					}
				}
				prop, found, err := unstructured.NestedMap(v, fields...)
				require.NoError(t, err)
				require.True(t, found, "%s %v", file, path)
				_, has := prop["maxLength"]
				require.True(t, has, "%s %v has a maxLength", file, path)
				delete(prop, "maxLength")
				// The CRD before v0.12 had no rules on these fields either
				// (ciRunURL's, #2158, cost too much unbounded).
				delete(prop, "x-kubernetes-validations")
				require.NoError(t, unstructured.SetNestedMap(v, prop, fields...))
			}
			versions[i] = v
		}
		require.NoError(t, unstructured.SetNestedSlice(doc, versions, "spec", "versions"))
		raw, err = yaml.Marshal(doc)
		require.NoError(t, err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	return &crd
}

// TestFreeTextLimitsRatchetOnAPIServer: the maxLength limits v0.12 put on a
// version's author and CI run URL, a gate override's reason and stage and a
// ChangeWindow's reason refuse new over-long text, and objects stored over
// the limit before the upgrade stay writable while the field is unchanged
// (CRD validation ratcheting, GA since Kubernetes 1.33; the chart needs
// 1.35): a label change, an end time change, a new expression all go
// through. Changing the over-long field itself is refused. The overrides
// list is atomic, so its entries are not correlated: a gate holding an
// over-long override takes no new override until that entry is dropped. CI
// runs it on the oldest and newest supported API servers.
// Covers CRD-RATCHET-01.
func TestFreeTextLimitsRatchetOnAPIServer(t *testing.T) {
	require.NotEmpty(t, os.Getenv("KUBEBUILDER_ASSETS"), "KUBEBUILDER_ASSETS names the envtest binaries (make envtest-install)")
	files := []string{"kardinal.io_bundles.yaml", "kardinal.io_policygates.yaml", "kardinal.io_changewindows.yaml"}
	var old []*apiextensionsv1.CustomResourceDefinition
	for _, f := range files {
		old = append(old, loadCRD(t, f, true))
	}
	env := &envtest.Environment{CRDs: old}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	dyn, err := dynamic.NewForConfig(cfg)
	require.NoError(t, err)
	ext, err := apiextensionsclient.NewForConfig(cfg)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Create(ctx,
		&unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "Namespace",
			"metadata": map[string]interface{}{"name": "team-a"}}}, metav1.CreateOptions{})
	require.NoError(t, err)

	long := strings.Repeat("a", 3000)
	gvr := func(resource string) schema.GroupVersionResource {
		return schema.GroupVersionResource{Group: "kardinal.io", Version: "v1alpha1", Resource: resource}
	}
	bundles := dyn.Resource(gvr("bundles")).Namespace("team-a")
	gates := dyn.Resource(gvr("policygates")).Namespace("team-a")
	windows := dyn.Resource(gvr("changewindows"))
	bundle := func(name, author string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "Bundle",
			"metadata": map[string]interface{}{"name": name, "namespace": "team-a"},
			"spec": map[string]interface{}{"type": "image", "pipeline": "app",
				"images":     []interface{}{map[string]interface{}{"repository": "ghcr.io/acme/app", "tag": "1.0.0"}},
				"provenance": map[string]interface{}{"author": author, "ciRunURL": "https://ci.example.com/" + author}}}}
	}
	gate := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "PolicyGate",
		"metadata": map[string]interface{}{"name": "g", "namespace": "team-a"},
		"spec": map[string]interface{}{"expression": "true", "overrides": []interface{}{map[string]interface{}{
			"reason": long, "stage": strings.Repeat("s", 100), "expiresAt": "2030-01-01T00:00:00Z", "createdBy": "alice"}}}}}
	window := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "ChangeWindow",
		"metadata": map[string]interface{}{"name": "freeze"},
		"spec":     map[string]interface{}{"type": "blackout", "start": "2026-10-01T00:00:00Z", "end": "2026-10-02T00:00:00Z", "reason": long}}}

	// Before the upgrade: over-long text is stored.
	_, err = bundles.Create(ctx, bundle("old", long), metav1.CreateOptions{})
	require.NoError(t, err, "the CRD before v0.12 stores a long author")
	_, err = gates.Create(ctx, gate, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = windows.Create(ctx, window, metav1.CreateOptions{})
	require.NoError(t, err)

	// The upgrade: the v0.12 CRDs.
	for _, f := range files {
		crd := loadCRD(t, f, false)
		cur, err := ext.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{})
		require.NoError(t, err)
		crd.ResourceVersion = cur.ResourceVersion
		_, err = ext.ApiextensionsV1().CustomResourceDefinitions().Update(ctx, crd, metav1.UpdateOptions{})
		require.NoError(t, err, f)
	}
	// The API server serves the new schema once a new long author is refused.
	require.Eventually(t, func() bool {
		_, err := bundles.Create(ctx, bundle("probe", long), metav1.CreateOptions{})
		if err == nil {
			_ = bundles.Delete(ctx, "probe", metav1.DeleteOptions{})
		}
		return apierrors.IsInvalid(err)
	}, 30*time.Second, 200*time.Millisecond, "the v0.12 CRD refuses a new author over 256 characters")

	// A stored object stays writable while its long field is unchanged.
	update := func(res dynamic.ResourceInterface, name string, edit func(u *unstructured.Unstructured)) error {
		u, err := res.Get(ctx, name, metav1.GetOptions{})
		require.NoError(t, err)
		edit(u)
		_, err = res.Update(ctx, u, metav1.UpdateOptions{})
		return err
	}
	assert.NoError(t, update(bundles, "old", func(u *unstructured.Unstructured) { u.SetLabels(map[string]string{"team": "a"}) }),
		"a Bundle with a long author takes a label change")
	assert.NoError(t, update(gates, "g", func(u *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(u.Object, "1 == 1", "spec", "expression")
	}), "a gate with a long override reason takes a new expression")
	assert.NoError(t, update(windows, "freeze", func(u *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(u.Object, "2026-10-03T00:00:00Z", "spec", "end")
	}), "a ChangeWindow with a long reason takes a new end")
	// Changing the long field itself is refused.
	err = update(bundles, "old", func(u *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(u.Object, long+"b", "spec", "provenance", "author")
	})
	assert.True(t, apierrors.IsInvalid(err), "a changed author over the limit is refused: %v", err)
	err = update(windows, "freeze", func(u *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(u.Object, long+"b", "spec", "reason")
	})
	assert.True(t, apierrors.IsInvalid(err), "a changed reason over the limit is refused: %v", err)
	// A new override next to the old over-long one: the overrides list is
	// atomic, so its old entry is not correlated and is validated again.
	err = update(gates, "g", func(u *unstructured.Unstructured) {
		list, _, _ := unstructured.NestedSlice(u.Object, "spec", "overrides")
		list = append(list, map[string]interface{}{"reason": "short", "expiresAt": "2030-01-01T00:00:00Z", "createdBy": "bob"})
		_ = unstructured.SetNestedSlice(u.Object, list, "spec", "overrides")
	})
	assert.True(t, apierrors.IsInvalid(err), "a new override next to an over-long one is refused: %v", err)
	// Dropping the over-long entry (an expired override is audit history,
	// kept in status.overrides) lets the new one in.
	assert.NoError(t, update(gates, "g", func(u *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(u.Object, []interface{}{map[string]interface{}{"reason": "short",
			"expiresAt": "2030-01-01T00:00:00Z", "createdBy": "bob"}}, "spec", "overrides")
	}), "without the over-long entry the new override is admitted")
}
