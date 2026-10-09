//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestGraph_ObjectsOnlyKardinal: the chart's graph-objects policy lets only
// the promotion Graph (kro) and kardinal's controller create or change
// PromotionSteps and PRStatuses, status included. A user with full RBAC on
// them cannot create a PromotionStep, set a step's state, write its
// spec.live gate results, or mark a PRStatus merged; neither can the test's
// cluster admin (without impersonation), a token minted for the namespace's
// Graph ServiceAccount, or a Pod running as it: only kro, which impersonates
// that ServiceAccount, is the Graph. The promotion runs as before.
//
// Covers GRAPH-OBJECTS-01.
func TestGraph_ObjectsOnlyKardinal(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)

	mallory := impersonating(t, e, a.ns, "mallory@example.com", nil,
		[]string{"promotionsteps", "promotionsteps/status", "prstatuses", "prstatuses/status"},
		"get", "list", "create", "update", "patch")
	refused := func(err error, what string) {
		t.Helper()
		require.Error(t, err, what)
		assert.True(t, apierrors.IsForbidden(err), "%s: %v", what, err)
		assert.Contains(t, err.Error(), "only kardinal (the promotion Graph or the controller) creates or changes this object", what)
	}
	refused(mallory.Status().Patch(ctx, ps.DeepCopy(), client.RawPatch(types.MergePatchType,
		[]byte(`{"status":{"state":"Verified"}}`))), "set a step's state")
	refused(mallory.Patch(ctx, ps.DeepCopy(), client.RawPatch(types.MergePatchType,
		[]byte(`{"spec":{"live":{"gates":[{"name":"forged","ready":true}]}}}`))), "write spec.live")
	refused(mallory.Create(ctx, &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "forged-step", Namespace: a.ns, Labels: map[string]string{
			"kardinal.io/pipeline": pipelineName, "kardinal.io/bundle": bundle, "kardinal.io/environment": "prod"}},
		Spec: v1alpha1.PromotionStepSpec{PipelineName: pipelineName, BundleName: bundle, Environment: "prod",
			StepType: "kustomize-set-image"},
	}), "create a PromotionStep")
	require.NotEmpty(t, ps.Spec.PRStatusRef)
	var prs v1alpha1.PRStatus
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: ps.Spec.PRStatusRef}, &prs))
	refused(mallory.Status().Patch(ctx, prs.DeepCopy(), client.RawPatch(types.MergePatchType,
		[]byte(`{"status":{"merged":true}}`))), "mark the PR merged")
	refused(e.Client.Status().Patch(ctx, prs.DeepCopy(), client.RawPatch(types.MergePatchType,
		[]byte(`{"status":{"merged":true}}`))), "the cluster admin marks the PR merged")

	// The Graph ServiceAccount lives in this namespace: an editor can mint
	// its token or run a Pod as it. Neither passes as kro, which
	// impersonates it.
	asGraph := func(token string) client.Client {
		cfg := rest.CopyConfig(e.Config)
		cfg.Impersonate = rest.ImpersonationConfig{}
		cfg.BearerToken, cfg.BearerTokenFile = token, ""
		cfg.CertData, cfg.KeyData, cfg.CertFile, cfg.KeyFile = nil, nil, "", ""
		cfg.ExecProvider, cfg.AuthProvider = nil, nil
		c, err := client.New(cfg, client.Options{Scheme: e.Client.Scheme()})
		require.NoError(t, err)
		return c
	}
	const graphSA = "kardinal-graph"
	minted, err := e.Kube.CoreV1().ServiceAccounts(a.ns).CreateToken(ctx, graphSA, &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](600)}}, metav1.CreateOptions{})
	require.NoError(t, err)
	// Writes the Graph ServiceAccount's RBAC allows (kro applies spec), so
	// only the admission policy refuses them.
	refused(asGraph(minted.Status.Token).Patch(ctx, ps.DeepCopy(), client.RawPatch(types.MergePatchType,
		[]byte(`{"spec":{"live":{"gates":[{"name":"forged","ready":true}]}}}`))), "a token minted for the Graph ServiceAccount (kubectl create token)")
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "as-graph", Namespace: a.ns},
		Spec: corev1.PodSpec{ServiceAccountName: graphSA, Containers: []corev1.Container{{Name: "pause",
			Image: fixtures.Pause + ":3.10", ImagePullPolicy: corev1.PullIfNotPresent}}}}
	require.NoError(t, e.Client.Create(ctx, pod))
	podToken, err := e.Kube.CoreV1().ServiceAccounts(a.ns).CreateToken(ctx, graphSA, &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](600),
			BoundObjectRef: &authenticationv1.BoundObjectReference{Kind: "Pod", APIVersion: "v1", Name: pod.Name, UID: pod.UID}}},
		metav1.CreateOptions{})
	require.NoError(t, err)
	refused(asGraph(podToken.Status.Token).Patch(ctx, prs.DeepCopy(), client.RawPatch(types.MergePatchType,
		[]byte(`{"metadata":{"labels":{"e2e":"forged"}}}`))), "a Pod running as the Graph ServiceAccount")

	cur, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "WaitingForMerge", cur.Status.State, "nothing was forged")
	pr := a.openPR(t, bundle, "prod")
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
}

// TestGraph_GateSquatterRefused: kro adopts an existing object of the name
// it applies, keeping the fields its template does not set. A PolicyGate
// created ahead of a Bundle under the name its gate instance will have, by
// someone who may only create PolicyGates, with an override in their name,
// does not become the instance: kro's adoption is refused, prod waits, and
// once the squatter is deleted kro creates the real instance, without the
// pre-seeded override, which blocks as its template says.
//
// Covers GRAPH-OBJECTS-02.
func TestGraph_GateSquatterRefused(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	e.CreateGate(t, framework.Gate(a.ns, "hold", "prod", "false", recheck))
	a.apply(t, a.pipeline(nil))

	bundleName := fmt.Sprintf("squat-%d", time.Now().UnixNano()%1_000_000)
	instance := fmt.Sprintf("hold-%s-prod--%s", a.ns, bundleName) // graph names.go gateNodeK8sName
	mallory := impersonating(t, e, a.ns, "mallory@example.com", nil, []string{"policygates"}, "get", "create")
	in := metav1.NewTime(time.Now().Add(time.Hour))
	squat := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: instance, Namespace: a.ns, Labels: map[string]string{"kardinal.io/environment": "prod"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "false", Generated: true, Overrides: []v1alpha1.PolicyGateOverride{{
			Reason: "pre-seeded", ExpiresAt: in, CreatedBy: "mallory@example.com"}}},
	}
	require.NoError(t, mallory.Create(ctx, squat), "a PolicyGate without kardinal.io/bundle is a template anyone may create")

	e.CreateBundleObject(t, &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: bundleName, Namespace: a.ns},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: pipelineName, Images: podinfoImages(fixtures.V2)},
	})
	e.WaitStepState(t, a.ns, pipelineName, bundleName, "test", "Verified", promoteTimeout)
	framework.Consistently(t, 20*time.Second, "kro does not adopt the squatter", func(ctx context.Context) (bool, string) {
		var g v1alpha1.PolicyGate
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: instance}, &g); err != nil {
			return false, err.Error()
		}
		return g.Labels["kardinal.io/bundle"] == "", fmt.Sprintf("labels %v", g.Labels)
	})
	e.NoStep(t, a.ns, pipelineName, bundleName, "prod", 5*time.Second)

	require.NoError(t, e.Client.Delete(ctx, squat))
	gate := e.WaitGateReady(t, a.ns, bundleName, "prod", "hold", false, "= false", gateTimeout)
	assert.Equal(t, instance, gate.Name)
	assert.Empty(t, gate.Spec.Overrides, "the squatter's override did not survive")
	raw, err := json.Marshal(gate.Labels)
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(raw), bundleName), "the real instance: %s", raw)
	e.NoStep(t, a.ns, pipelineName, bundleName, "prod", 5*time.Second)
}
