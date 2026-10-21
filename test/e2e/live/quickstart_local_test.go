//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// kubeconfigFlag is the --kubeconfig the commands quickstart prints carry
// when the shell sets KUBECONFIG.
var kubeconfigFlag = regexp.MustCompile(` --kubeconfig \S+`)

// apiextensionsCRDList lists CustomResourceDefinitions as metadata.
var apiextensionsCRDList = schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinitionList"}

// TestQuickstartLocal_FromNothing (#1785) runs docs/quickstart.md as a new
// user without a GitHub account runs it, on a kind cluster with nothing
// installed (the quickstart-local suite): kardinal quickstart --local
// installs kro, Argo CD, the in-cluster git server and kardinal, seeds the
// demo repository and ships the first version to prod's gate; kardinal
// approve demo prod promotes it, and prod runs it from the local
// repository. Nothing is exposed: every Service of the git server is a
// ClusterIP, and neither token appears in any output. Then kardinal
// quickstart --local --delete, which without a terminal needs --yes, lists
// and removes everything the quickstart created: nothing else uses it.
//
// The quickstart block runs with this checkout's chart and images (a
// development CLI would install the newest published chart); otherwise
// the commands are the page's.
//
// Covers QUICKSTART-LOCAL-01, QUICKSTART-LOCAL-02.
func TestQuickstartLocal_FromNothing(t *testing.T) {
	e := framework.NewWithoutGit(t)
	ctx := context.Background()
	const page, app = "quickstart.md", "kardinal-test-app"
	image, render := os.Getenv(framework.EnvImage), os.Getenv("KARDINAL_E2E_RENDER_IMAGE")
	require.NotEmpty(t, image, "hack/e2e/up.sh quickstart-local builds the controller image")
	require.NotEmpty(t, render)
	set := func(key, ref string) string {
		i := strings.LastIndex(ref, ":")
		return fmt.Sprintf(" --set %s.repository=%s --set %s.tag=%s --set %s.pullPolicy=Never", key, ref[:i], key, ref[i+1:], key)
	}
	checkout := `kardinal quickstart --local --chart "$KARDINAL_E2E_CHART"` + set("image", image) + set("render.image", render)

	// Nothing there yet: no namespace the quickstart would create.
	for _, ns := range []string{"kro-system", "argocd", "kardinal-system", "kardinal-git"} {
		var n corev1.Namespace
		require.True(t, apierrors.IsNotFound(e.Client.Get(ctx, types.NamespacedName{Name: ns}, &n)), "namespace %s exists before the quickstart", ns)
	}

	// A failure from here on logs what the demo saw (#2083), then removes
	// what the quickstart created, so that the next iteration of a count-N
	// run starts from nothing again instead of failing on this one's
	// leftovers.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		quickstartLocalDiagnostics(t, e)
		e.ShellEnvFor(t, 15*time.Minute, nil, framework.Fill(t, framework.PageBlock(t, page, "kardinal quickstart --local --delete"),
			"--local --delete", "--local --delete --yes"))
		waitQuickstartLocalGone(t, e, app)
	})

	// ── 3. Run the quickstart ───────────────────────────────────────────
	block := framework.Fill(t, framework.PageBlockAt(t, page, "kardinal quickstart --local", 0), "kardinal quickstart --local", checkout)
	r := e.ShellEnvFor(t, 30*time.Minute, nil, block)
	require.Zero(t, r.Code, "docs/%s, kardinal quickstart --local:\n%s", page, r.Output)
	// The commands it prints name the suite's context and the kubeconfig the
	// shell set (KUBECONFIG); the page shows kind's context alone.
	out := kubeconfigFlag.ReplaceAllString(strings.ReplaceAll(r.Output, e.Context, "kind-kardinal"), "")
	for _, want := range []string{
		"kro 0.10.0-rc.0: installed",
		"Argo CD v3.5.3: installed",
		"Git server kardinal-git/kardinal-git (http://kardinal-git.kardinal-git.svc.cluster.local:3000/kardinal/kardinal-demo.git): installed, repository kardinal/kardinal-demo seeded",
		"Secret kardinal-system/kardinal-scm-token: created (the controller's token on the git server, for PRs)",
		"Secret default/git-token: created (the Pipeline's token on the git server)",
		"prod: Held by gate prod-approval",
		"kubectl --context kind-kardinal -n kardinal-git port-forward svc/kardinal-git 3000",
	} {
		assert.Contains(t, out, want)
	}
	// The page shows this output: the lines it shows are the ones printed.
	var shown []string
	for _, l := range strings.Split(framework.PageSample(t, page, "kardinal quickstart --local", 0), "\n") {
		if strings.Contains(l, "Git server ") || strings.Contains(l, "Secret ") || strings.Contains(l, "PolicyGate default/prod-approval") ||
			strings.Contains(l, "Pipeline default/demo") || strings.HasPrefix(strings.TrimSpace(l), "prod: Held by gate") || l == "Next:" {
			shown = append(shown, l)
		}
	}
	require.Greater(t, len(shown), 5, "the page shows quickstart --local's output")
	assert.Empty(t, framework.SampleMismatches(strings.Join(shown, "\n"), out, nil, "demo"), "quickstart --local output:\n%s", out)

	// Nothing exposed, no credential out of the cluster.
	var svcs corev1.ServiceList
	require.NoError(t, e.Client.List(ctx, &svcs, client.InNamespace("kardinal-git")))
	require.NotEmpty(t, svcs.Items)
	for _, s := range svcs.Items {
		assert.Equal(t, corev1.ServiceTypeClusterIP, s.Spec.Type, "Service %s", s.Name)
		for _, p := range s.Spec.Ports {
			assert.Zero(t, p.NodePort, "Service %s port %s", s.Name, p.Name)
		}
	}
	for _, key := range []types.NamespacedName{{Namespace: "kardinal-system", Name: "kardinal-scm-token"}, {Namespace: "default", Name: "git-token"}} {
		var s corev1.Secret
		require.NoError(t, e.Client.Get(ctx, key, &s))
		tok := string(s.Data["token"])
		require.Len(t, tok, 40, "Secret %s holds a token of the git server", key)
		assert.NotContains(t, out, tok, "Secret %s's token in the quickstart's output", key)
	}

	var p v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: "default", Name: "demo"}, &p))
	assert.Equal(t, "http://kardinal-git.kardinal-git.svc.cluster.local:3000/kardinal/kardinal-demo.git", p.Spec.Git.URL)
	first := newestBundle(t, e, "default", "demo")
	for _, env := range []string{"test", "uat"} {
		e.WaitStepState(t, "default", "demo", first, env, "Verified", time.Minute)
		e.WaitDeploymentImage(t, app+"-"+env, app, demoShipImage, syncTimeout)
	}

	// ── 5. Approve ──────────────────────────────────────────────────────
	r = e.ShellEnvFor(t, 5*time.Minute, nil, framework.PageBlockAt(t, page, "kardinal approve demo prod", 0))
	require.Zero(t, r.Code, r.Output)
	assert.Contains(t, r.Output, "Recorded: ")
	e.WaitBundlePhase(t, "default", first, "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, app+"-prod", app, demoShipImage, syncTimeout)
	var b v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: "default", Name: first}, &b))
	t.Logf("version %s Verified in test, uat and prod from the in-cluster repository", first)

	// ── Clean up: quickstart --local --delete ───────────────────────────
	// Without a terminal it goes ahead only with --yes; it lists what it
	// removes first.
	del := framework.PageBlock(t, page, "kardinal quickstart --local --delete")
	r = e.ShellEnvFor(t, 2*time.Minute, nil, del)
	require.NotZero(t, r.Code, "--local --delete without a terminal or --yes:\n%s", r.Output)
	assert.Contains(t, r.Output, "pass --yes; nothing was deleted")
	r = e.ShellEnvFor(t, 15*time.Minute, nil, framework.Fill(t, del, "--local --delete", "--local --delete --yes"))
	require.Zero(t, r.Code, "kardinal quickstart --local --delete --yes:\n%s", r.Output)
	for _, want := range []string{
		"remove namespaces kardinal-test-app-test, kardinal-test-app-uat, kardinal-test-app-prod (the Applications')",
		"uninstall kardinal-promoter with its ", "uninstall Argo CD v3.5.3 with its ", "uninstall kro 0.10.0-rc.0 with its ",
		"remove the git server and its repository (namespace kardinal-git)",
	} {
		assert.Contains(t, r.Output, want)
	}
	waitQuickstartLocalGone(t, e, app)
}

// waitQuickstartLocalGone waits until everything quickstart --local created
// is deleted: its namespaces (app's included), the kardinal, kro and Argo CD
// CRDs, and the demo's ConfigMaps.
func waitQuickstartLocalGone(t *testing.T, e *framework.Env, app string) {
	t.Helper()
	gone := []string{"kardinal-git", "kro-system", "argocd", "kardinal-system", app + "-test", app + "-uat", app + "-prod"}
	framework.Eventually(t, 5*time.Minute, "everything the quickstart created deleted", func(ctx context.Context) (bool, string) {
		var left []string
		for _, ns := range gone {
			var n corev1.Namespace
			if err := e.Client.Get(ctx, types.NamespacedName{Name: ns}, &n); err == nil {
				left = append(left, ns)
			}
		}
		var crds metav1.PartialObjectMetadataList
		crds.SetGroupVersionKind(apiextensionsCRDList)
		if err := e.Client.List(ctx, &crds); err != nil {
			return false, err.Error()
		}
		for _, c := range crds.Items {
			if strings.HasSuffix(c.Name, ".kardinal.io") || strings.HasSuffix(c.Name, ".kro.run") || strings.HasSuffix(c.Name, ".argoproj.io") {
				left = append(left, "crd/"+c.Name)
			}
		}
		var cms corev1.ConfigMapList
		if err := e.Client.List(ctx, &cms, client.InNamespace("default"), client.MatchingLabels{"kardinal.io/quickstart": "demo"}); err != nil {
			return false, err.Error()
		}
		for _, c := range cms.Items {
			left = append(left, "configmap/"+c.Name)
		}
		return len(left) == 0, strings.Join(left, ", ")
	})
}

// quickstartLocalDiagScript reads, without changing anything, what a failed
// quickstart --local left: the steps' messages, kardinal why, the Argo CD
// Applications' sync and health, every pod not ready (described) and every
// Warning event, the pods and events of the demo's namespaces, and the logs
// of Argo CD and the controller. Each command runs even when one before it
// fails.
const quickstartLocalDiagScript = `set +e
kubectl version
kubectl get promotionsteps -A -o custom-columns=NAME:.metadata.name,STATE:.status.state,MESSAGE:.status.message
kardinal why demo
kubectl get applications.argoproj.io -A -o wide
kubectl -n argocd get applications.argoproj.io -o yaml | grep -A40 '^  status:' | head -200
kubectl get pods -A -o wide
kubectl get pods -A --no-headers | awk '{split($3, r, "/"); if (r[1] != r[2] && $4 != "Completed") print $1, $2}' |
  while read -r ns pod; do kubectl -n "$ns" describe pod "$pod" | tail -40; done
kubectl get events -A --field-selector type=Warning --sort-by=.lastTimestamp | tail -80
for ns in kardinal-test-app-test kardinal-test-app-uat kardinal-test-app-prod; do
  kubectl -n "$ns" describe pods | tail -60
  kubectl -n "$ns" get events --sort-by=.lastTimestamp | tail -40
done
kubectl -n argocd logs deploy/argocd-repo-server --tail=60
kubectl -n argocd logs statefulset/argocd-application-controller --tail=80
kubectl -n kardinal-system logs deploy/kardinal-promoter --tail=200 | grep -iE 'health|error|kardinal-test-app' | tail -60
kubectl -n kardinal-git get pods,svc -o wide
true`

// quickstartLocalDiagnostics logs quickstartLocalDiagScript's output for a
// failed run. The test has no git server of the suite's, so the framework's
// namespace diagnostics collect nothing for it, and a run that failed in
// test's health check (#2083, 1.35) left nothing to read.
func quickstartLocalDiagnostics(t *testing.T, e *framework.Env) {
	t.Helper()
	e.ShellEnvFor(t, 3*time.Minute, nil, quickstartLocalDiagScript)
}
