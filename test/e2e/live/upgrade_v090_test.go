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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// guideChartV090 is the chart reference of the "Upgrading from v0.9.0"
// guide: the test replaces it with the checkout's chart.
const guideChartV090 = "oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.10.0"

// TestUpgradeV090_FromV090 follows "Upgrading from v0.9.0" in
// docs/upgrade.md, running the guide's own commands, on a cluster that
// runs kardinal-promoter v0.9.0 as released (its chart from the registry,
// its CLI and kro at the version v0.9.0 targets).
//
// Under v0.9.0: Pipeline "promote" (test auto, prod pr-review) has a Bundle
// that is Verified in test with its prod PR open: in flight; Pipeline "done"
// has a Bundle that is Verified. The git Secret is not labeled
// referenceable, as v0.9.0 did not ask for it.
//
// After the guide's steps (label the git Secrets, apply the new CRDs,
// re-run install-kro.sh, helm upgrade --reset-then-reuse-values, kardinal
// doctor):
//   - the git Secret is labeled and the Pipelines are Ready, with no
//     SecretReferenceable=False condition;
//   - the Verified Bundle stays Verified;
//   - the in-flight Bundle keeps its Graph, PromotionSteps and PRStatus
//     (same uid), and finishes once its prod PR is merged;
//   - the chart's ValidatingAdmissionPolicies refuse no write to the objects
//     v0.9.0 stored: a label on each Pipeline, Bundle and PolicyGate is
//     admitted, and no Graph reports a refused write;
//   - a new Bundle promotes end to end.
//
// Covers UPG-V090-01, UPG-V090-02.
func TestUpgradeV090_FromV090(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	minor := serverMinor(t, e)
	t.Logf("Kubernetes 1.%d", minor)

	a := newArgoApp(t, e, "test", "prod", "done")
	ns := a.ns

	// v0.9.0 did not ask for the referenceable label on git Secrets.
	var secret corev1.Secret
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: framework.GitSecretName}, &secret))
	delete(secret.Labels, "kardinal.io/referenceable")
	require.NoError(t, e.Client.Update(ctx, &secret))

	// ── v0.9.0 ──────────────────────────────────────────────────────────
	all := a.pipeline(map[string]string{"prod": "pr-review"})
	promote := all.DeepCopy()
	promote.Name = "promote"
	promote.Spec.Environments = all.Spec.Environments[:2]
	done := all.DeepCopy()
	done.Name = "done"
	done.Spec.Environments = all.Spec.Environments[2:]
	done.Spec.Environments[0].Approval = "auto"
	gate := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "always", Namespace: ns,
		Labels: map[string]string{"kardinal.io/applies-to": "prod"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", Message: "a gate v0.9.0 stored"}}
	for _, o := range []client.Object{promote, done, gate} {
		require.NoError(t, e.Client.Create(ctx, o), "%T %s under v0.9.0", o, o.GetName())
	}

	v090Bundle := func(pipeline, image string) string {
		t.Helper()
		out, err := e.KardinalV090(t, ns, "create", "bundle", pipeline, "--image", image)
		require.NoError(t, err, out)
		m := v081BundleCreated.FindStringSubmatch(out)
		require.NotNil(t, m, out)
		return m[1]
	}
	bDone := v090Bundle("done", imageV2)
	e.WaitBundlePhase(t, ns, bDone, "Verified", promoteTimeout)
	b1 := v090Bundle("promote", imageV2)
	e.WaitStepState(t, ns, "promote", b1, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, ns, "promote", b1, "prod", "WaitingForMerge", promoteTimeout)
	// v0.9.0 named the PR branch kardinal/<bundle>/<env>, without the
	// namespace hash this release adds.
	pr := e.WaitPR(t, a.repo, time.Minute, "the open prod PR of "+b1+" (v0.9.0)", func(pr gitserver.PR) bool {
		return pr.State == "open" && pr.Head == "kardinal/"+b1+"/prod"
	})
	t.Logf("v0.9.0 Bundles: %s (Verified), %s (in flight, prod PR #%d)", bDone, b1, pr.Number)

	graphOf := func(bundle string) (name string, uid types.UID) {
		t.Helper()
		list, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		for _, g := range list.Items {
			if g.GetLabels()["kardinal.io/bundle"] == bundle {
				return g.GetName(), g.GetUID()
			}
		}
		return "", ""
	}
	g1, g1uid := graphOf(b1)
	require.NotEmpty(t, g1, "the in-flight Bundle has its Graph under v0.9.0")
	steps, err := e.Steps(ctx, ns, "promote", b1)
	require.NoError(t, err)
	inFlight := []client.Object{u090Get(t, e, ns, "prstatus-"+b1+"-prod", &v1alpha1.PRStatus{})}
	for i := range steps {
		inFlight = append(inFlight, &steps[i])
	}
	kept := keep(inFlight...)
	doneSteps, err := e.Steps(ctx, ns, "done", bDone)
	require.NoError(t, err)

	// ── 2. Label the Pipelines' git Secrets ─────────────────────────────
	e.MustShell(t, framework.GuideBlockFrom(t, "0.9.0", "kardinal.io/referenceable=true"))
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: framework.GitSecretName}, &secret))
	assert.Equal(t, "true", secret.Labels["kardinal.io/referenceable"], "the guide labels the git Secret")

	// ── 3. Apply the new CRDs ───────────────────────────────────────────
	crds := framework.Fill(t, framework.GuideBlockFrom(t, "0.9.0", "helm show crds"), guideChartV090, `"$KARDINAL_E2E_CHART"`)
	e.MustShell(t, crds)

	// ── 4. Upgrade kro ──────────────────────────────────────────────────
	e.MustShell(t, framework.Fill(t, framework.GuideBlockFrom(t, "0.9.0", "install-kro.sh"), "<your-context>", e.Context))

	// ── 5. Upgrade the chart ────────────────────────────────────────────
	rel := e.ExistingRelease(t, upgradeRelease, framework.ControllerNamespace)
	upgradeCmd := strings.TrimRight(framework.Fill(t, framework.GuideBlockFrom(t, "0.9.0", "helm upgrade"), guideChartV090, `"$KARDINAL_E2E_CHART"`), "\n") + ` \
  --set image.repository="${KARDINAL_E2E_IMAGE%:*}" --set image.tag="${KARDINAL_E2E_IMAGE##*:}" --set image.pullPolicy=Never
`
	up := e.Shell(t, upgradeCmd)
	require.Zero(t, up.Code, up.Output)
	rel.WaitRolledOut(t, 3*time.Minute)

	// ── 6. Check the result ─────────────────────────────────────────────
	check := e.MustShell(t, framework.GuideBlockFrom(t, "0.9.0", "kardinal doctor"))
	assert.Regexp(t, `(?m)^\d+ check\(s\) passed$`, check, "kardinal doctor passes every check")

	for _, name := range []string{"promote", "done"} {
		framework.Eventually(t, time.Minute, "Pipeline "+name+" Ready, git Secret referenceable", func(ctx context.Context) (bool, string) {
			var p v1alpha1.Pipeline
			if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &p); err != nil {
				return false, err.Error()
			}
			ready := meta.FindStatusCondition(p.Status.Conditions, "Ready")
			ref := meta.FindStatusCondition(p.Status.Conditions, "SecretReferenceable")
			return ready != nil && ready.Status == metav1.ConditionTrue && (ref == nil || ref.Status != metav1.ConditionFalse),
				fmt.Sprintf("conditions %v", p.Status.Conditions)
		})
	}

	// The Verified Bundle stays Verified; its steps are not run again.
	e.WaitBundlePhase(t, ns, bDone, "Verified", time.Minute)
	for _, s := range doneSteps {
		var now v1alpha1.PromotionStep
		require.NoError(t, e.Client.Get(ctx, client.ObjectKeyFromObject(&s), &now), "%s survives the upgrade", s.Name)
		assert.Equal(t, "Verified", now.Status.State, "%s stays Verified", s.Name)
		assert.Equal(t, s.UID, now.UID, "%s is the same step", s.Name)
	}

	// The in-flight Bundle keeps its Graph (rebuilt in place, same uid) and
	// its steps and PRStatus.
	name, uid := graphOf(b1)
	assert.Equal(t, g1, name, "the in-flight Bundle keeps its Graph")
	assert.Equal(t, g1uid, uid, "the Graph is updated in place, not recreated")
	u090AssertKept(t, e, kept)

	// The new ValidatingAdmissionPolicies refuse no write to the objects
	// v0.9.0 stored.
	vaps := e.Kubectl(t, "", "", "get", "validatingadmissionpolicybindings", "-o", "name")
	assert.Contains(t, vaps, "kardinal-promoter-", "the chart's admission policies are installed")
	for _, obj := range []client.Object{
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "promote"}},
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "done"}},
		&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: b1}},
		&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: bDone}},
		&v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "always"}},
	} {
		patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":{"e2e.kardinal.io/after-upgrade":"true"}}}`))
		assert.NoError(t, e.Client.Patch(ctx, obj, patch), "label %T %s after the upgrade", obj, obj.GetName())
	}

	// The in-flight Bundle finishes.
	a.merge(t, pr)
	e.WaitStepState(t, ns, "promote", b1, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, ns, b1, "Verified", time.Minute)
	a.running(t, "prod", imageV2, "the in-flight Bundle deployed prod after the upgrade")

	// No Graph reports a write the admission policies refused.
	list, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for _, g := range list.Items {
		assert.NotContains(t, fmt.Sprint(g.Object["status"]), "ValidatingAdmissionPolicy", "Graph %s", g.GetName())
	}

	// A new Bundle promotes end to end.
	b2 := e.CreateBundle(t, ns, "promote", "--image", imageV3)
	e.WaitStepState(t, ns, "promote", b2, "test", "Verified", promoteTimeout)
	a.running(t, "test", imageV3, "the new Bundle deployed test")
	a.merge(t, a.openPR(t, b2, "prod"))
	e.WaitStepState(t, ns, "promote", b2, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, ns, b2, "Verified", time.Minute)
	a.running(t, "prod", imageV3, "the new Bundle deployed prod")
}

// u090Get reads ns/name into obj.
func u090Get(t *testing.T, e *framework.Env, ns, name string, obj client.Object) client.Object {
	t.Helper()
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, obj))
	return obj
}

// u090AssertKept checks that each object still exists with the same uid and
// creationTimestamp.
func u090AssertKept(t *testing.T, e *framework.Env, objs []kept) {
	t.Helper()
	(&upgrade{e: e}).assertKept(t, objs)
}
