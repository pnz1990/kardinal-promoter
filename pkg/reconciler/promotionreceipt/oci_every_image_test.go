// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionreceipt

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kube-openapi/pkg/validation/strfmt"
	"k8s.io/kube-openapi/pkg/validation/validate"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
)

// receiptSchemaErrors are the reasons the API server would refuse rc
// against the generated PromotionReceipt CRD (types, enums, patterns,
// lengths, item counts): what the fake client does not check.
func receiptSchemaErrors(t *testing.T, rc *v1alpha1.PromotionReceipt) []string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	require.True(t, ok)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", "..", "config", "crd", "bases", "kardinal.io_promotionreceipts.yaml"))
	require.NoError(t, err)
	var crd apiextensionsv1.CustomResourceDefinition
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	var internal apiextensionsinternal.JSONSchemaProps
	require.NoError(t, apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(
		crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &internal, nil))
	s, err := structuralschema.NewStructural(&internal)
	require.NoError(t, err)
	obj, err := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(rc)
	require.NoError(t, err)
	obj["apiVersion"], obj["kind"] = "kardinal.io/v1alpha1", "PromotionReceipt"
	var out []string
	for _, e := range validate.NewSchemaValidator(s.ToKubeOpenAPI(), nil, "", strfmt.Default).Validate(obj).Errors {
		out = append(out, e.Error())
	}
	return out
}

// TestReconcile_OCIEveryImage (#2331): a receipt of a Bundle with 100
// images (spec.images' bound), with the OCI sink, is pushed as a referrer
// of every image and reaches Stored, PR comment and all; its status, the CR
// and an OCI entry per image, is one the API server accepts. With
// status.stored capped at 16 the write after the referrers was refused
// every time.
//
// Covers RECEIPT-OCI-04.
func TestReconcile_OCIEveryImage(t *testing.T) {
	const n = 100
	srv := httptest.NewServer(registry.New(registry.WithReferrersSupport(true), registry.Logger(log.New(io.Discard, "", 0))))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	var images []v1alpha1.ImageRef
	for i := range n {
		repo := fmt.Sprintf("%s/acme/svc-%03d", host, i)
		img, err := random.Image(16, 1)
		require.NoError(t, err)
		ref, err := name.ParseReference(repo+":1", name.Insecure)
		require.NoError(t, err)
		require.NoError(t, remote.Write(ref, img))
		d, err := img.Digest()
		require.NoError(t, err)
		images = append(images, v1alpha1.ImageRef{Repository: repo, Digest: d.String()})
	}
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1-5-0", Namespace: "payments", UID: "u1"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Images: images},
	}
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Namespace: "payments"},
		Spec:       v1alpha1.PromotionStepSpec{PipelineName: "checkout", BundleName: b.Name, Environment: "prod"},
	}
	stmt, d, err := receipt.Build(receipt.Input{Name: "checkout-1-5-0-prod", ClusterID: "c1", Now: now, Bundle: b, Step: ps})
	require.NoError(t, err)
	rc := &v1alpha1.PromotionReceipt{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1-5-0-prod", Namespace: "payments"},
		Spec: v1alpha1.PromotionReceiptSpec{Statement: string(stmt), StatementDigest: d,
			PullRequest: &v1alpha1.ReceiptPullRequest{Repo: "acme/deploy", Number: 412}},
	}
	secret, _ := keySecret(t)
	h := newHarness(t, secret, rc)
	h.r.OCI = &OCISink{Registry: receipt.Registry{Insecure: true}}
	got := h.run(t, 4)

	assert.Equal(t, v1alpha1.ReceiptStored, got.Status.Phase, "conditions %v", got.Status.Conditions)
	assert.True(t, got.Status.PRComment)
	require.Len(t, got.Status.Stored, n+1, "the CR and a referrer for each image")
	subjects := map[string]bool{}
	for _, s := range got.Status.Stored[1:] {
		assert.Equal(t, v1alpha1.ReceiptSinkOCI, s.Sink)
		subjects[s.Subject] = true
	}
	assert.Len(t, subjects, n, "one referrer per image")
	assert.Empty(t, receiptSchemaErrors(t, got), "the API server accepts the stored receipt")
}

// TestReconcile_OCIRepositoryChangedMidStore (QA-3 on #2335): the OCI sink's
// repository changes while a receipt of 100 images is part-way through
// storing (the registry fails the push after half of them). The referrers
// pushed to the old repository no longer count, so every image is pushed
// to the new one, and each new entry replaces its subject's old one: the
// receipt reaches Stored with one entry per image, all in the new
// repository, and a status the CRD schema accepts. Appended instead, the
// entries ran past the bound (the CR, 50 old and 100 new) and the receipt
// stayed Signed.
//
// Covers RECEIPT-OCI-05.
func TestReconcile_OCIRepositoryChangedMidStore(t *testing.T) {
	const n, half = 100, 50
	var manifests, allow atomic.Int64
	allow.Store(half)
	reg := registry.New(registry.WithReferrersSupport(true), registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPut && strings.Contains(req.URL.Path, "/manifests/") && manifests.Add(1) > allow.Load() {
			http.Error(w, "the registry is down", http.StatusInternalServerError)
			return
		}
		reg.ServeHTTP(w, req)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	images := make([]v1alpha1.ImageRef, n)
	for i := range images {
		images[i] = v1alpha1.ImageRef{Repository: fmt.Sprintf("registry.example/acme/svc-%03d", i), Digest: fmt.Sprintf("sha256:%064x", i+1)}
	}
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1-5-0", Namespace: "payments", UID: "u1"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Images: images},
	}
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Namespace: "payments"},
		Spec:       v1alpha1.PromotionStepSpec{PipelineName: "checkout", BundleName: b.Name, Environment: "prod"},
	}
	stmt, d, err := receipt.Build(receipt.Input{Name: "checkout-1-5-0-prod", ClusterID: "c1", Now: now, Bundle: b, Step: ps})
	require.NoError(t, err)
	secret, _ := keySecret(t)
	h := newHarness(t, secret, &v1alpha1.PromotionReceipt{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1-5-0-prod", Namespace: "payments"},
		Spec:       v1alpha1.PromotionReceiptSpec{Statement: string(stmt), StatementDigest: d},
	})
	h.r.OCI = &OCISink{Registry: receipt.Registry{Insecure: true}, Repository: host + "/receipts-old"}
	got := h.run(t, 2)
	require.Equal(t, v1alpha1.ReceiptSigned, got.Status.Phase, "the push failed part-way")
	require.Len(t, got.Status.Stored, 1+half, "the CR and the referrers pushed before the failure")

	// The operator moves the sink to another repository; the registry is
	// back, and the retry is due.
	allow.Store(1 << 30)
	h.r.OCI = &OCISink{Registry: receipt.Registry{Insecure: true}, Repository: host + "/receipts-new"}
	require.NotNil(t, got.Status.NextRetryAt)
	later := got.Status.NextRetryAt.Add(time.Second)
	h.r.NowFn = func() time.Time { return later }
	got = h.run(t, 3)
	assert.Equal(t, v1alpha1.ReceiptStored, got.Status.Phase, "conditions %v", got.Status.Conditions)
	require.Len(t, got.Status.Stored, 1+n, "the CR and one referrer per image")
	subjects := map[string]bool{}
	for _, s := range got.Status.Stored[1:] {
		assert.True(t, strings.HasPrefix(s.Ref, host+"/receipts-new@sha256:"), "in the new repository: %s", s.Ref)
		subjects[s.Subject] = true
	}
	assert.Len(t, subjects, n)
	assert.Empty(t, receiptSchemaErrors(t, got), "the API server accepts the stored receipt")
}
