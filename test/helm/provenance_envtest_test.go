//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"fmt"
	"os"
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
)

// withoutCIRunURLRules is the Bundle CRD before #2158: no rules on
// provenance.ciRunURL.
func withoutCIRunURLRules(t *testing.T) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	crd := loadCRD(t, "kardinal.io_bundles.yaml", false)
	for i := range crd.Spec.Versions {
		spec := crd.Spec.Versions[i].Schema.OpenAPIV3Schema.Properties["spec"]
		prov := spec.Properties["provenance"]
		u := prov.Properties["ciRunURL"]
		require.NotEmpty(t, u.XValidations, "the CRD has ciRunURL rules")
		u.XValidations = nil
		prov.Properties["ciRunURL"] = u
		spec.Properties["provenance"] = prov
		crd.Spec.Versions[i].Schema.OpenAPIV3Schema.Properties["spec"] = spec
	}
	return crd
}

// TestBundleCIRunURLCredentialsOnAPIServer (#2158): a Bundle written to the
// API server directly (kubectl apply, not the Bundle API) is refused when
// provenance.ciRunURL holds user info or a credential parameter in its
// query or fragment, whatever its case; a URL without one is stored. A
// Bundle stored before the rule stays writable while the URL is unchanged
// (CRD validation ratcheting), so the controller is never stuck on it, and
// its receipt redacts the URL (TestReceiptJourney_TokenInProvenance). CI
// runs it on the oldest and newest supported API servers.
//
// Covers RECEIPT-REDACT-02.
func TestBundleCIRunURLCredentialsOnAPIServer(t *testing.T) {
	require.NotEmpty(t, os.Getenv("KUBEBUILDER_ASSETS"), "KUBEBUILDER_ASSETS names the envtest binaries (make envtest-install)")
	env := &envtest.Environment{CRDs: []*apiextensionsv1.CustomResourceDefinition{withoutCIRunURLRules(t)}}
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
	bundles := dyn.Resource(schema.GroupVersionResource{Group: "kardinal.io", Version: "v1alpha1", Resource: "bundles"}).Namespace("team-a")
	bundle := func(name, ciRunURL string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "Bundle",
			"metadata": map[string]interface{}{"name": name, "namespace": "team-a"},
			"spec": map[string]interface{}{"type": "image", "pipeline": "app",
				"images":     []interface{}{map[string]interface{}{"repository": "ghcr.io/acme/app", "tag": "1.0.0"}},
				"provenance": map[string]interface{}{"author": "ci", "ciRunURL": ciRunURL}}}}
	}

	// Before the upgrade: a token in the URL is stored.
	_, err = bundles.Create(ctx, bundle("old", "https://ci.example.com/run/1?token=s3cret"), metav1.CreateOptions{})
	require.NoError(t, err)

	crd := loadCRD(t, "kardinal.io_bundles.yaml", false)
	cur, err := ext.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{})
	require.NoError(t, err)
	crd.ResourceVersion = cur.ResourceVersion
	_, err = ext.ApiextensionsV1().CustomResourceDefinitions().Update(ctx, crd, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, err := bundles.Create(ctx, bundle("probe", "https://ci.example.com/run/1?token=s3cret"), metav1.CreateOptions{})
		if err == nil {
			_ = bundles.Delete(ctx, "probe", metav1.DeleteOptions{})
		}
		return apierrors.IsInvalid(err)
	}, 30*time.Second, 200*time.Millisecond, "the new CRD refuses a token in ciRunURL")

	n := 0
	for name, tc := range map[string]struct {
		url  string
		want string
	}{
		"token":            {"https://ci.example.com/run/1?token=s3cret", "credential parameter"},
		"second parameter": {"https://ci.example.com/run/1?attempt=2&access_token=s3cret", "credential parameter"},
		"upper case":       {"https://ci.example.com/run/1?X-Amz-Signature=s3cret", "credential parameter"},
		"fragment":         {"https://ci.example.com/run/1#sig=s3cret", "credential parameter"},
		"semicolon":        {"https://ci.example.com/run/1?a=1;private_token=s3cret", "credential parameter"},
		"user info":        {"https://bot:s3cret@ci.example.com/run/1", "user info"},
		"host as user":     {"https://github.com@evil.example/run/1", "user info"},
		"plain":            {"https://github.com/acme/app/actions/runs/7?check_suite_focus=true", ""},
		"a longer name":    {"https://ci.example.com/run/1?tokens=3&monkey=1", ""},
	} {
		n++
		t.Run(name, func(t *testing.T) {
			_, err := bundles.Create(ctx, bundle(fmt.Sprintf("b-%d", n), tc.url), metav1.CreateOptions{})
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "%v", err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	// The Bundle stored before the rule stays writable while the URL is
	// unchanged; changing the URL to another one with a token is refused.
	u, err := bundles.Get(ctx, "old", metav1.GetOptions{})
	require.NoError(t, err)
	u.SetLabels(map[string]string{"team": "a"})
	u, err = bundles.Update(ctx, u, metav1.UpdateOptions{})
	require.NoError(t, err, "a stored Bundle with a token takes a label change")
	require.NoError(t, unstructured.SetNestedField(u.Object, "https://ci.example.com/run/2?token=s3cret", "spec", "provenance", "ciRunURL"))
	_, err = bundles.Update(ctx, u, metav1.UpdateOptions{})
	assert.True(t, apierrors.IsInvalid(err), "a new token is refused: %v", err)
}
