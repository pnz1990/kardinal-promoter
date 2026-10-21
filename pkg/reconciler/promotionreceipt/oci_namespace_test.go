// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionreceipt

import (
	"context"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
)

// TestParseNamespaceRepositories (#2285): --receipt-oci-namespace-repositories
// is namespace=entry,entry;namespace=entry, and a bad map is refused whole,
// so the controller stops at start instead of refusing receipts.
//
// Covers RECEIPT-OCI-06.
func TestParseNamespaceRepositories(t *testing.T) {
	got, err := ParseNamespaceRepositories(" team-a = ghcr.io/team-a/, docker.io/team-a ; team-b=registry.local:5000/b ;")
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"team-a": {"ghcr.io/team-a/", "docker.io/team-a"}, "team-b": {"registry.local:5000/b"}}, got)
	empty, err := ParseNamespaceRepositories("")
	require.NoError(t, err)
	assert.Empty(t, empty)
	for v, want := range map[string]string{
		"team-a":                         "namespace=repository",
		"Team_A=ghcr.io/a/":              "namespace \"Team_A\"",
		"team-a=ghcr.io/a/;team-a=x.io/": "listed twice",
		"team-a=":                        "no repositories",
		"team-a=ghcr.io/a/../b":          "is not a registry",
		"team-a=ghcr.io/A/":              "is not a registry",
	} {
		_, err := ParseNamespaceRepositories(v)
		require.Error(t, err, v)
		assert.Contains(t, err.Error(), want, v)
	}
	for _, m := range []string{PerNamespaceOff, PerNamespaceNarrow, PerNamespaceRequired} {
		assert.NoError(t, ValidatePerNamespace(m))
	}
	assert.Error(t, ValidatePerNamespace("strict"))
	assert.Error(t, ValidatePerNamespace(""))
}

// TestOCIScope_Allowed (#2285): where the OCI sink may push one namespace's
// referrers. The controller-wide list is always the outer bound; off keeps
// it alone (v0.13); narrow holds a namespace with entries to them; required
// also refuses a namespace with none; a fixed repository is the admin's
// own; an annotation entry that does not parse refuses the namespace.
//
// Covers RECEIPT-OCI-06.
func TestOCIScope_Allowed(t *testing.T) {
	const a, b, other = "ghcr.io/team-a/app", "ghcr.io/team-b/app", "quay.io/x/app"
	cluster := []string{"ghcr.io/"}
	for _, tc := range []struct {
		name    string
		sink    OCISink
		entries []string
		invalid string
		allow   map[string]bool
		why     string // part of the reason for a refusal
	}{
		{name: "off: the controller-wide list alone", sink: OCISink{PerNamespace: PerNamespaceOff, AllowedRepositories: cluster},
			entries: []string{"ghcr.io/team-a/"}, allow: map[string]bool{a: true, b: true, other: false},
			why: "sinks.oci.allowedRepositories"},
		{name: "off and no list: everything, as before", sink: OCISink{}, allow: map[string]bool{a: true, b: true, other: true}},
		{name: "narrow, with entries", sink: OCISink{PerNamespace: PerNamespaceNarrow, AllowedRepositories: cluster},
			entries: []string{"ghcr.io/team-a/"}, allow: map[string]bool{a: true, b: false, other: false},
			why: "namespace team-a's receipt OCI repositories"},
		{name: "narrow, no entries: the controller-wide list", sink: OCISink{PerNamespace: PerNamespaceNarrow, AllowedRepositories: cluster},
			allow: map[string]bool{a: true, b: true, other: false}},
		{name: "required, no entries: nothing", sink: OCISink{PerNamespace: PerNamespaceRequired, AllowedRepositories: cluster},
			allow: map[string]bool{a: false, b: false, other: false}, why: "has no receipt OCI repositories"},
		{name: "required, with entries", sink: OCISink{PerNamespace: PerNamespaceRequired},
			entries: []string{"ghcr.io/team-a/"}, allow: map[string]bool{a: true, b: false, other: false}},
		{name: "a namespace cannot widen the controller-wide list", sink: OCISink{PerNamespace: PerNamespaceRequired, AllowedRepositories: []string{"ghcr.io/team-a/"}},
			entries: []string{"ghcr.io/"}, allow: map[string]bool{a: true, b: false}, why: "sinks.oci.allowedRepositories"},
		{name: "a fixed repository is the admin's own", sink: OCISink{PerNamespace: PerNamespaceRequired, Repository: "ghcr.io/ops/receipts"},
			allow: map[string]bool{a: true, other: true}},
		{name: "an entry that does not parse refuses the namespace", sink: OCISink{PerNamespace: PerNamespaceNarrow},
			entries: []string{"ghcr.io/team-a/"}, invalid: `"ghcr.io/x/.." is not a registry`, allow: map[string]bool{a: false},
			why: "has an entry that does not parse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &ociScope{sink: &tc.sink, namespace: "team-a", entries: tc.entries, invalid: tc.invalid}
			for repo, want := range tc.allow {
				ok, why := s.allowed(repo)
				assert.Equal(t, want, ok, repo)
				if !ok && tc.why != "" && repo != other {
					assert.Contains(t, why, tc.why, repo)
				}
			}
		})
	}
}

// TestReconcile_OCIPerNamespace (#2285): two namespaces on one controller,
// each with a receipt of an image in team A's repository, with
// perNamespace required. Team A's Namespace lists the repository: its
// receipt gets the referrer. Team B's lists none: its receipt is signed and
// kept, Stored False RepositoryNotAllowed naming the namespace, not
// retried, and nothing is written to A's repository for it. With the
// annotation ignored, the controller's map alone decides. A Namespace that
// cannot be read pushes nothing and is retried.
//
// Covers RECEIPT-OCI-06.
func TestReconcile_OCIPerNamespace(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.WithReferrersSupport(true), registry.Logger(log.New(io.Discard, "", 0))))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	repoA := host + "/team-a/checkout"
	img, err := random.Image(64, 1)
	require.NoError(t, err)
	ref, err := name.ParseReference(repoA+":1", name.Insecure)
	require.NoError(t, err)
	require.NoError(t, remote.Write(ref, img))
	d, err := img.Digest()
	require.NoError(t, err)
	namespace := func(n, annotation string) *corev1.Namespace {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n}}
		if annotation != "" {
			ns.Annotations = map[string]string{AnnotationOCIRepositories: annotation}
		}
		return ns
	}
	receiptIn := func(ns string) *v1alpha1.PromotionReceipt {
		rc := newReceipt(t, repoA, d.String())
		rc.Namespace, rc.Spec.PullRequest = ns, nil
		return rc
	}
	run := func(h harness, ns string) *v1alpha1.PromotionReceipt {
		key := types.NamespacedName{Namespace: ns, Name: "checkout-1-5-0-prod"}
		for range 4 {
			_, err := h.r.Reconcile(context.Background(), ctrlruntime.Request{NamespacedName: key})
			require.NoError(t, err)
		}
		var rc v1alpha1.PromotionReceipt
		require.NoError(t, h.c.Get(context.Background(), key, &rc))
		return &rc
	}
	referrers := func() int {
		got, err := bundlesOf((&receipt.Registry{Insecure: true}).Referrers(context.Background(), repoA, d.String(), nil))
		require.NoError(t, err)
		return len(got)
	}
	ociStored := func(rc *v1alpha1.PromotionReceipt) bool {
		for _, s := range rc.Status.Stored {
			if s.Sink == v1alpha1.ReceiptSinkOCI {
				return true
			}
		}
		return false
	}
	secret, _ := keySecret(t)

	t.Run("the Namespace's annotation", func(t *testing.T) {
		h := newHarness(t, secret, namespace("team-a", host+"/team-a/"), namespace("team-b", ""), receiptIn("team-a"), receiptIn("team-b"))
		h.r.APIReader = h.c
		h.r.OCI = &OCISink{Registry: receipt.Registry{Insecure: true}, PerNamespace: PerNamespaceRequired, NamespaceAnnotation: true}
		before := referrers()
		rcA := run(h, "team-a")
		assert.Equal(t, metav1.ConditionTrue, conditionOf(rcA, ConditionStored).Status)
		assert.True(t, ociStored(rcA))
		assert.Equal(t, before+1, referrers(), "team A's receipt is pushed to team A's repository")

		rcB := run(h, "team-b")
		assert.Equal(t, v1alpha1.ReceiptStored, rcB.Status.Phase, "signed and kept in the CR")
		c := conditionOf(rcB, ConditionStored)
		assert.Equal(t, metav1.ConditionFalse, c.Status)
		assert.Equal(t, "RepositoryNotAllowed", c.Reason)
		assert.Contains(t, c.Message, repoA+": namespace team-b has no receipt OCI repositories")
		assert.Zero(t, rcB.Status.Attempts, "not retried")
		assert.False(t, ociStored(rcB))
		assert.Equal(t, before+1, referrers(), "nothing written to team A's repository for team B")
	})

	t.Run("the annotation ignored: the controller's map decides", func(t *testing.T) {
		h := newHarness(t, secret, namespace("team-a", ""), namespace("team-b", host+"/team-a/"), receiptIn("team-a"), receiptIn("team-b"))
		h.r.APIReader = h.c
		h.r.OCI = &OCISink{Registry: receipt.Registry{Insecure: true}, PerNamespace: PerNamespaceRequired,
			NamespaceRepositories: map[string][]string{"team-a": {host + "/team-a/"}}}
		assert.True(t, ociStored(run(h, "team-a")))
		rcB := run(h, "team-b")
		assert.False(t, ociStored(rcB), "team B's own annotation does not count")
		assert.Equal(t, "RepositoryNotAllowed", conditionOf(rcB, ConditionStored).Reason)
	})

	t.Run("a Namespace that cannot be read", func(t *testing.T) {
		inner := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(secret, namespace("team-a", host+"/team-a/"), receiptIn("team-a")).
			WithStatusSubresource(&v1alpha1.PromotionReceipt{}).
			WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Namespace); ok {
					return apierrors.NewServiceUnavailable("etcd is down")
				}
				return c.Get(ctx, key, obj, opts...)
			}}).Build()
		h := newHarness(t)
		h.c, h.r.Client, h.r.APIReader = inner, inner, inner
		h.r.Signer.Key = SecretKey(inner, "kardinal-system", "receipt-key", "cosign.key")
		h.r.OCI = &OCISink{Registry: receipt.Registry{Insecure: true}, PerNamespace: PerNamespaceNarrow, NamespaceAnnotation: true}
		rc := run(h, "team-a")
		assert.Equal(t, v1alpha1.ReceiptSigned, rc.Status.Phase, "not Stored")
		c := conditionOf(rc, ConditionStored)
		assert.Equal(t, "NamespaceUnreadable", c.Reason)
		assert.Equal(t, 1, rc.Status.Attempts, "retried with backoff")
		assert.False(t, ociStored(rc), "nothing pushed while the Namespace cannot be read")
	})
}

// TestOCIScopeFor_Sources (#2285): a namespace's entries are the
// controller's map's and, unless ignored, its Namespace's annotation's,
// together; with perNamespace off or a fixed repository the Namespace is
// not read at all; an annotation entry that does not parse is recorded,
// refusing the namespace.
//
// Covers RECEIPT-OCI-06.
func TestOCIScopeFor_Sources(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a",
		Annotations: map[string]string{AnnotationOCIRepositories: " docker.io/team-a ,ghcr.io/team-a/extra"}}}
	bad := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b",
		Annotations: map[string]string{AnnotationOCIRepositories: "ghcr.io/team-b/,ghcr.io/x/.."}}}
	reads := 0
	plain := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-c"}}
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(ns, bad, plain).
		WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Namespace); ok {
				reads++
			}
			return c.Get(ctx, key, obj, opts...)
		}}).Build()
	scopeOf := func(sink OCISink, n string) *ociScope {
		r := &Reconciler{Client: c, APIReader: c, OCI: &sink}
		s, err := r.ociScopeFor(context.Background(), n)
		require.NoError(t, err)
		return s
	}
	both := scopeOf(OCISink{PerNamespace: PerNamespaceNarrow, NamespaceAnnotation: true,
		NamespaceRepositories: map[string][]string{"team-a": {"ghcr.io/team-a/"}}}, "team-a")
	assert.Equal(t, []string{"ghcr.io/team-a/", "docker.io/team-a", "ghcr.io/team-a/extra"}, both.entries)
	assert.Empty(t, both.invalid)
	mapOnly := scopeOf(OCISink{PerNamespace: PerNamespaceNarrow, NamespaceRepositories: map[string][]string{"team-a": {"ghcr.io/team-a/"}}}, "team-a")
	assert.Equal(t, []string{"ghcr.io/team-a/"}, mapOnly.entries, "the annotation ignored")
	assert.Nil(t, scopeOf(OCISink{PerNamespace: PerNamespaceRequired, NamespaceAnnotation: true}, "team-c").entries, "no entries at all")
	assert.Nil(t, scopeOf(OCISink{PerNamespace: PerNamespaceRequired, NamespaceAnnotation: true}, "gone").entries, "a Namespace gone has none")
	invalid := scopeOf(OCISink{PerNamespace: PerNamespaceNarrow, NamespaceAnnotation: true}, "team-b")
	assert.Contains(t, invalid.invalid, `"ghcr.io/x/.."`)
	if ok, why := invalid.allowed("ghcr.io/team-b/app"); assert.False(t, ok) {
		assert.Contains(t, why, "has an entry that does not parse")
	}

	reads = 0
	scopeOf(OCISink{PerNamespace: PerNamespaceOff, NamespaceAnnotation: true}, "team-a")
	scopeOf(OCISink{PerNamespace: PerNamespaceRequired, NamespaceAnnotation: true, Repository: "ghcr.io/ops/receipts"}, "team-a")
	assert.Zero(t, reads, "off, or a fixed repository: the Namespace is not read")
}
