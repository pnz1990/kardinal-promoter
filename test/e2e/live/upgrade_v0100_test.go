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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// guideChartV0100 is the chart reference of the "Upgrading from v0.10.0"
// guide: the test replaces it with the checkout's chart.
const guideChartV0100 = "oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.0"

// TestUpgradeV0100_FromV0100 follows "Upgrading from v0.10.0" in
// docs/upgrade.md, running the guide's own commands, on a cluster that runs
// kardinal-promoter v0.10.0 as released (its chart from the registry, its
// CLI and kro at the version v0.10.0 targets).
//
// Under v0.10.0: Pipeline "promote" (test auto, prod pr-review) has a Bundle
// that is Verified in test with its prod PR open: in flight; Pipeline "done"
// has a Bundle that is Verified. The controller's SCM token Secret carries
// kardinal.io/referenceable, as a user who labelled it for the demo
// Pipeline left it. The Pipelines' git Secret is not labeled referenceable,
// which v0.10.0 accepted with a warning. NotificationHook "plain" sets the
// removed spec.webhook.authorizationHeader.
//
// After the guide's steps (remove the controller Secret's label, move the
// hook's header into a Secret, apply the new CRDs, helm upgrade
// --reset-then-reuse-values) the git Secret is still unlabeled, as when a
// user skipped step 2: both Pipelines are Ready=False SecretNotReferenceable
// with the label command. Then step 2 labels it, and kardinal doctor passes:
//   - the controller's Secret has no referenceable label;
//   - hook "plain" reads its header from Secret plain-auth (key
//     authorization, labeled referenceable) through spec.webhook.secretRef,
//     and is Ready after the upgrade;
//   - the Pipelines are Ready and get status.environments and
//     status.summary, which kubectl get pipelines shows as SUMMARY;
//   - the Verified Bundle stays Verified with the same steps, and has no
//     status.version; the in-flight Bundle gets its version;
//   - the in-flight Bundle keeps its Graph, PromotionSteps and PRStatus
//     (same uid), and finishes once its prod PR is merged;
//   - the chart's admission policies admit a label on each object v0.10.0
//     stored, and no Graph reports a refused write;
//   - a new Bundle promotes end to end, with its version.
//
// Covers UPG-V0100-01, UPG-V0100-02.
func TestUpgradeV0100_FromV0100(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	minor := serverMinor(t, e)
	t.Logf("Kubernetes 1.%d", minor)

	a := newArgoApp(t, e, "test", "prod", "done")
	ns := a.ns

	// A user labelled the controller's own SCM token Secret referenceable.
	var ctrlSecret corev1.Secret
	ctrlKey := types.NamespacedName{Namespace: framework.ControllerNamespace, Name: framework.GitSecretName}
	require.NoError(t, e.Client.Get(ctx, ctrlKey, &ctrlSecret))
	if ctrlSecret.Labels == nil {
		ctrlSecret.Labels = map[string]string{}
	}
	ctrlSecret.Labels["kardinal.io/referenceable"] = "true"
	require.NoError(t, e.Client.Update(ctx, &ctrlSecret))

	// v0.10.0 used an unlabeled git Secret, with a warning.
	var gitSecret corev1.Secret
	gitKey := types.NamespacedName{Namespace: ns, Name: framework.GitSecretName}
	require.NoError(t, e.Client.Get(ctx, gitKey, &gitSecret))
	delete(gitSecret.Labels, "kardinal.io/referenceable")
	require.NoError(t, e.Client.Update(ctx, &gitSecret))

	// ── v0.10.0 ─────────────────────────────────────────────────────────
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
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", Message: "a gate v0.10.0 stored"}}
	// v0.10.0's NotificationHook still has authorizationHeader; this
	// checkout's type does not, so the hook is written as unstructured.
	hook := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kardinal.io/v1alpha1", "kind": "NotificationHook",
		"metadata": map[string]interface{}{"name": "plain", "namespace": ns},
		"spec": map[string]interface{}{
			"webhook": map[string]interface{}{"url": "https://hooks.example.invalid/plain", "authorizationHeader": "Bearer PLAINTOKEN"},
			"events":  []interface{}{"Bundle.Failed"},
		},
	}}
	for _, o := range []client.Object{promote, done, gate, hook} {
		require.NoError(t, e.Client.Create(ctx, o), "%T %s under v0.10.0", o, o.GetName())
	}

	v0100Bundle := func(pipeline, image string) string {
		t.Helper()
		out, err := e.KardinalV0100(t, ns, "create", "bundle", pipeline, "--image", image)
		require.NoError(t, err, out)
		m := v081BundleCreated.FindStringSubmatch(out)
		require.NotNil(t, m, out)
		return m[1]
	}
	bDone := v0100Bundle("done", imageV2)
	e.WaitBundlePhase(t, ns, bDone, "Verified", promoteTimeout)
	b1 := v0100Bundle("promote", imageV2)
	e.WaitStepState(t, ns, "promote", b1, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, ns, "promote", b1, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "the open prod PR of "+b1+" (v0.10.0)", func(pr gitserver.PR) bool {
		return pr.State == "open" && strings.HasPrefix(pr.Head, "kardinal/") && strings.Contains(pr.Head, b1) && strings.HasSuffix(pr.Head, "/prod")
	})
	t.Logf("v0.10.0 Bundles: %s (Verified), %s (in flight, prod PR #%d %s)", bDone, b1, pr.Number, pr.Head)

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
	require.NotEmpty(t, g1, "the in-flight Bundle has its Graph under v0.10.0")
	steps, err := e.Steps(ctx, ns, "promote", b1)
	require.NoError(t, err)
	inFlight := []client.Object{u090Get(t, e, ns, "prstatus-"+b1+"-prod", &v1alpha1.PRStatus{})}
	for i := range steps {
		inFlight = append(inFlight, &steps[i])
	}
	kept := keep(inFlight...)
	doneSteps, err := e.Steps(ctx, ns, "done", bDone)
	require.NoError(t, err)

	// ── 3. Take the label off the controller's SCM token Secret ─────────
	e.MustShell(t, framework.GuideBlockFrom(t, "0.10.0", "kardinal.io/referenceable-"))
	require.NoError(t, e.Client.Get(ctx, ctrlKey, &ctrlSecret))
	assert.NotContains(t, ctrlSecret.Labels, "kardinal.io/referenceable", "the guide takes the label off the controller's Secret")
	// The step is safe to run again, with the label already gone.
	e.MustShell(t, framework.GuideBlockFrom(t, "0.10.0", "kardinal.io/referenceable-"))

	// ── 4. Move the hook's Authorization header into a Secret ───────────
	e.MustShell(t, framework.GuideBlockFrom(t, "0.10.0", "authorizationHeader"))
	var authSecret corev1.Secret
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: "plain-auth"}, &authSecret))
	assert.Equal(t, "Bearer PLAINTOKEN", string(authSecret.Data["authorization"]))
	assert.Equal(t, "true", authSecret.Labels["kardinal.io/referenceable"])
	migrated := e.Kubectl(t, ns, "", "get", "notificationhook", "plain", "-o", "jsonpath={.spec.webhook}")
	assert.Contains(t, migrated, `"secretRef":{"name":"plain-auth"}`)
	assert.NotContains(t, migrated, "authorizationHeader")
	// With nothing left to move, the step changes nothing.
	assert.Empty(t, strings.TrimSpace(e.MustShell(t, framework.GuideBlockFrom(t, "0.10.0", "authorizationHeader"))))

	// ── 5. Apply the new CRDs ───────────────────────────────────────────
	e.MustShell(t, framework.Fill(t, framework.GuideBlockFrom(t, "0.10.0", "helm show crds"), guideChartV0100, `"$KARDINAL_E2E_CHART"`))

	// ── 6. Upgrade the chart ────────────────────────────────────────────
	rel := e.ExistingRelease(t, upgradeRelease, framework.ControllerNamespace)
	upgradeCmd := strings.TrimRight(framework.Fill(t, framework.GuideBlockFrom(t, "0.10.0", "helm upgrade"), guideChartV0100, `"$KARDINAL_E2E_CHART"`), "\n") + ` \
  --set image.repository="${KARDINAL_E2E_IMAGE%:*}" --set image.tag="${KARDINAL_E2E_IMAGE##*:}" --set image.pullPolicy=Never
`
	up := e.Shell(t, upgradeCmd)
	require.Zero(t, up.Code, up.Output)
	rel.WaitRolledOut(t, 3*time.Minute)

	// The git Secret is still unlabeled: this release does not use it.
	for _, name := range []string{"promote", "done"} {
		framework.Eventually(t, 2*time.Minute, "Pipeline "+name+" Ready=False SecretNotReferenceable", func(ctx context.Context) (bool, string) {
			var got v1alpha1.Pipeline
			if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
				return false, err.Error()
			}
			c := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "SecretNotReferenceable" &&
					strings.Contains(c.Message, "kubectl label secret "+framework.GitSecretName+" -n "+ns+" kardinal.io/referenceable=true") &&
					meta.FindStatusCondition(got.Status.Conditions, "SecretReferenceable") == nil,
				fmt.Sprintf("conditions %v", got.Status.Conditions)
		})
	}

	// ── 2. Label the Pipelines' git Secrets (skipped before the upgrade) ─
	e.MustShell(t, framework.GuideBlockFrom(t, "0.10.0", "kardinal.io/referenceable=true --overwrite"))
	require.NoError(t, e.Client.Get(ctx, gitKey, &gitSecret))
	assert.Equal(t, "true", gitSecret.Labels["kardinal.io/referenceable"], "the guide labels the git Secret")

	// ── 7. Check the result ─────────────────────────────────────────────
	check := e.MustShell(t, framework.GuideBlockFrom(t, "0.10.0", "kardinal doctor"))
	assert.Regexp(t, `(?m)^\d+ check\(s\) passed$`, check, "kardinal doctor passes every check")

	framework.Eventually(t, time.Minute, "hook plain Ready with its Secret", func(ctx context.Context) (bool, string) {
		var h v1alpha1.NotificationHook
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: "plain"}, &h); err != nil {
			return false, err.Error()
		}
		c := meta.FindStatusCondition(h.Status.Conditions, "Ready")
		return c != nil && c.Status == metav1.ConditionTrue && meta.FindStatusCondition(h.Status.Conditions, "PlaintextCredential") == nil,
			fmt.Sprintf("conditions %v", h.Status.Conditions)
	})

	// Within a minute of the label every Pipeline is Ready and has the
	// status projection.
	for _, p := range []*v1alpha1.Pipeline{promote, done} {
		name, envs := p.Name, len(p.Spec.Environments)
		framework.Eventually(t, 2*time.Minute, "Pipeline "+name+" Ready with status.environments and summary", func(ctx context.Context) (bool, string) {
			var got v1alpha1.Pipeline
			if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
				return false, err.Error()
			}
			ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			return ready != nil && ready.Status == metav1.ConditionTrue && got.Status.Summary != "" && len(got.Status.Environments) == envs,
				fmt.Sprintf("conditions %v, summary %q, %d environments", got.Status.Conditions, got.Status.Summary, len(got.Status.Environments))
		})
	}
	pipelines := e.Kubectl(t, ns, "", "get", "pipelines")
	assert.Contains(t, pipelines, "SUMMARY", "kubectl get pipelines shows the summary column")

	// The Verified Bundle stays Verified; its steps are not run again, and
	// it gets no status.version (settled Bundles are not backfilled).
	e.WaitBundlePhase(t, ns, bDone, "Verified", time.Minute)
	for _, s := range doneSteps {
		var now v1alpha1.PromotionStep
		require.NoError(t, e.Client.Get(ctx, client.ObjectKeyFromObject(&s), &now), "%s survives the upgrade", s.Name)
		assert.Equal(t, "Verified", now.Status.State, "%s stays Verified", s.Name)
		assert.Equal(t, s.UID, now.UID, "%s is the same step", s.Name)
	}
	var settled v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: bDone}, &settled))
	assert.Empty(t, settled.Status.Version, "a Bundle that finished before the upgrade shows no VERSION")

	// The in-flight Bundle keeps its Graph (same uid), steps and PRStatus,
	// and gets its version.
	name, uid := graphOf(b1)
	assert.Equal(t, g1, name, "the in-flight Bundle keeps its Graph")
	assert.Equal(t, g1uid, uid, "the Graph is updated in place, not recreated")
	u090AssertKept(t, e, kept)
	framework.Eventually(t, time.Minute, "the in-flight Bundle gets status.version", func(ctx context.Context) (bool, string) {
		var b v1alpha1.Bundle
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: b1}, &b); err != nil {
			return false, err.Error()
		}
		return b.Status.Version != "", fmt.Sprintf("phase %s, version %q", b.Status.Phase, b.Status.Version)
	})

	// The admission policies refuse no write to the objects v0.10.0 stored.
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

	// A new Bundle promotes end to end and has its version.
	b2 := e.CreateBundle(t, ns, "promote", "--image", imageV3)
	e.WaitStepState(t, ns, "promote", b2, "test", "Verified", promoteTimeout)
	a.running(t, "test", imageV3, "the new Bundle deployed test")
	a.merge(t, a.openPR(t, b2, "prod"))
	e.WaitStepState(t, ns, "promote", b2, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, ns, b2, "Verified", time.Minute)
	a.running(t, "prod", imageV3, "the new Bundle deployed prod")
	var fresh v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: b2}, &fresh))
	assert.NotEmpty(t, fresh.Status.Version, "a Bundle created after the upgrade has its version")
}
