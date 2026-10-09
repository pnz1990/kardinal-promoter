//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// whoAmI is the username the API server authenticates the test's kube
// context as, which is what the CLI records (SelfSubjectReview).
func whoAmI(t *testing.T, e *framework.Env) string {
	t.Helper()
	r, err := e.Kube.AuthenticationV1().SelfSubjectReviews().Create(context.Background(),
		&authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, r.Status.UserInfo.Username)
	return r.Status.UserInfo.Username
}

// impersonating returns a client that acts as user (with groups), after
// granting user verbs on resources of kardinal.io in ns, so the request
// reaches admission.
func impersonating(t *testing.T, e *framework.Env, ns, user string, groups []string, resources []string, verbs ...string) client.Client {
	t.Helper()
	ctx := context.Background()
	name := "e2e-" + strings.NewReplacer(":", "-", "@", "-", ".", "-").Replace(user)
	_, err := e.Kube.RbacV1().Roles(ns).Create(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{"kardinal.io"}, Resources: resources, Verbs: verbs}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = e.Kube.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: user}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	cfg := rest.CopyConfig(e.Config)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: groups}
	c, err := client.New(cfg, client.Options{Scheme: e.Client.Scheme()})
	require.NoError(t, err)
	return c
}

// TestBundle_Reject rejects a Bundle whose prod PR waits for a merge, with
// kardinal reject. The Bundle turns Rejected with the Ready and Rejected
// conditions naming the CLI's authenticated user and the reason; its prod
// step fails as rejected, its PR is closed, and a PromotionRejected
// AuditEvent is written. test keeps the change (reject does not revert), the
// steps stay as history and no new step appears. spec.rejected cannot be
// removed or changed, and a second reject is refused.
//
// Covers BUNDLE-REJECT-01, BUNDLE-REJECT-02.
func TestBundle_Reject(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	ctx := context.Background()

	b := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, b, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, b, "prod", "WaitingForMerge", promoteTimeout)
	pr := a.openPR(t, b, "prod")

	user := whoAmI(t, e)
	out := e.MustKardinal(t, a.ns, "reject", b, "--reason", "e2e: CVE in the base image")
	assert.Contains(t, out, fmt.Sprintf("Bundle %s rejected by %s (was Promoting): e2e: CVE in the base image", b, user))

	got := e.WaitBundlePhase(t, a.ns, b, "Rejected", time.Minute)
	require.NotNil(t, got.Spec.Rejected)
	assert.Equal(t, user, got.Spec.Rejected.By, "spec.rejected.by is the authenticated user, not the OS user")
	assert.NotNil(t, got.Spec.Rejected.At)
	want := fmt.Sprintf("rejected by %s: e2e: CVE in the base image; it is never promoted again", user)
	ready := findCond(got.Status.Conditions, "Ready")
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, "Rejected", ready.Reason)
	rej := findCond(got.Status.Conditions, "Rejected")
	assert.Equal(t, metav1.ConditionTrue, rej.Status)
	assert.Equal(t, want, rej.Message)

	cancelled := e.WaitStepState(t, a.ns, pipelineName, b, "prod", "Failed", time.Minute)
	assert.Equal(t, fmt.Sprintf("bundle %s was rejected — promotion cancelled", b), cancelled.Status.Message)
	e.WaitPRState(t, a.repo, pr.Number, "closed", time.Minute)
	assert.Contains(t, stepAudits(t, e, a.ns, b, "prod"), "PromotionRejected")

	// Reject does not revert: test keeps the change, and its step stays
	// Verified as history.
	a.running(t, "test", imageV2, "reject leaves a delivered change in place")
	e.WaitStepState(t, a.ns, pipelineName, b, "test", "Verified", time.Minute)
	framework.Consistently(t, 15*time.Second, "the rejected Bundle gets no new step and stays Rejected", func(ctx context.Context) (bool, string) {
		var cur v1alpha1.Bundle
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: b}, &cur); err != nil {
			return false, err.Error()
		}
		n := a.stepCount(t, b)
		return n == 2 && cur.Status.Phase == "Rejected", fmt.Sprintf("steps=%d phase=%s", n, cur.Status.Phase)
	})

	// One-way: the CRD refuses to remove or change spec.rejected, and the
	// CLI refuses a second rejection.
	err := e.Client.Patch(ctx, got.DeepCopy(), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"rejected":null}}`)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.rejected cannot be removed")
	err = e.Client.Patch(ctx, got.DeepCopy(), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"rejected":{"reason":"changed"}}}`)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.rejected is immutable once set")
	out, err = e.Kardinal(t, a.ns, "reject", b, "--reason", "again")
	require.Error(t, err)
	assert.Contains(t, out, fmt.Sprintf("bundle %s was already rejected by %s: e2e: CVE in the base image", b, user))
}

// TestBundle_RejectIdentity checks the chart's bundle-rejection
// ValidatingAdmissionPolicy against real users (impersonated, with patch and
// create on Bundles): a rejection in someone else's name is denied on create
// and on update, one in the requester's own name is admitted and the Bundle
// turns Rejected, and an unrelated Bundle write is not checked.
//
// Covers BUNDLE-REJECT-03.
func TestBundle_RejectIdentity(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	ctx := context.Background()
	alice := impersonating(t, e, ns, "alice@example.com", []string{"release-managers"},
		[]string{"bundles"}, "get", "create", "patch")

	// The Pipeline does not exist, so the controller only records
	// PipelineNotFound until the Bundle is rejected.
	newBundle := func(name string, rej *v1alpha1.BundleRejection) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "no-such-pipeline", Rejected: rej,
				Images: []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}}},
		}
	}
	err := alice.Create(ctx, newBundle("forged", &v1alpha1.BundleRejection{By: "bob@example.com", Reason: "x"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `spec.rejected.by must be your own username "alice@example.com", not "bob@example.com"`)

	require.NoError(t, alice.Create(ctx, newBundle("victim", nil)), "a Bundle without a rejection is not checked")
	forged := []byte(`{"spec":{"rejected":{"by":"bob@example.com","reason":"x"}}}`)
	err = alice.Patch(ctx, newBundle("victim", nil), client.RawPatch(types.MergePatchType, forged))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.rejected.by must be your own username")

	own := []byte(`{"spec":{"rejected":{"by":"alice@example.com","reason":"e2e own rejection"}}}`)
	require.NoError(t, alice.Patch(ctx, newBundle("victim", nil), client.RawPatch(types.MergePatchType, own)))
	got := e.WaitBundlePhase(t, ns, "victim", "Rejected", time.Minute)
	assert.Equal(t, "rejected by alice@example.com: e2e own rejection; it is never promoted again",
		findCond(got.Status.Conditions, "Rejected").Message)
}

// TestRollback_SkipsRejected promotes three Bundles to test and rejects the
// middle one after it was Verified there. kardinal rollback from the newest
// goes back past the rejected Bundle to the oldest, which runs again, and
// rollback --to the rejected Bundle is refused.
//
// Covers RB-REJECT-01.
func TestRollback_SkipsRejected(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))

	b1 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	rbVerified(t, a, b1, "test")
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV3)
	rbVerified(t, a, b2, "test")
	b3 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV1)
	rbVerified(t, a, b3, "test")

	e.MustKardinal(t, a.ns, "reject", b2, "--reason", "e2e: bad build")
	e.WaitBundlePhase(t, a.ns, b2, "Rejected", time.Minute)
	assert.Equal(t, fmt.Sprintf("rollback: bundle %s was rejected, so it is never promoted again; pick another Bundle: invalid request", b2),
		rbRefused(t, a, "--env", "test", "--to", b2))

	out, rb := rbRollback(t, a, "test")
	assert.Contains(t, out, fmt.Sprintf("from %s to %s", b3, b1), "the rollback skips the rejected %s", b2)
	rbAssertBundle(t, e, a.ns, rb, "test", b3, b1, cliUser(t), "")
	rbVerified(t, a, rb, "test")
	assertEnvAt(t, a, "test", fixtures.V2)
}
