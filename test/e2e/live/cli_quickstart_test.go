//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package live

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// demoNames serializes the tests that use the cluster-wide names of
// kardinal-demo: the ApplicationSet kardinal-test-app in argocd, its
// Applications kardinal-test-app-<env> and their namespaces.
var demoNames sync.Mutex

// lockDemoNames holds demoNames until the test and its cleanups end.
func lockDemoNames(t *testing.T) {
	t.Helper()
	demoNames.Lock()
	t.Cleanup(demoNames.Unlock)
}

// TestCLI_Quickstart runs kardinal quickstart on the suite's kind cluster,
// where kro, Argo CD and kardinal already run, over a repo laid out like
// pnz1990/kardinal-demo.
//
// First it refuses a context that is not a local cluster (by name, or by
// server), before it writes anything. Then it applies the git Secret, the
// ApplicationSet, the approval gate (allowed: the caller, from
// SelfSubjectReview) and the Pipeline demo in default, ships the first
// version and follows it until prod waits for the approval, and prints the
// next steps. kardinal approve demo prod ships it to prod. A re-run changes
// nothing and ships nothing.
// Covers CLI-QUICKSTART-01, CLI-QUICKSTART-02.
func TestCLI_Quickstart(t *testing.T) {
	t.Parallel()
	lockDemoNames(t)
	e := framework.New(t)
	ctx := context.Background()
	cli := e.CLI(t)
	ns := e.Namespace(t)
	const app = "kardinal-test-app"
	envs := []string{"test", "uat", "prod"}
	workloadNS := func(env string) string { return app + "-" + env }
	repo := e.Repo(t, ns, demoLayoutRepo(envs, app))
	require.Equal(t, "main", repo.Branch, "quickstart's ApplicationSet deploys main")
	token, err := e.Kube.CoreV1().Secrets(ns).Get(ctx, framework.GitSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	withToken := framework.CLIOptions{Env: []string{"GITHUB_TOKEN=" + string(token.Data["token"])}}
	image := fixtures.Image + ":" + fixtures.V2

	// Cleanup runs last first: what quickstart created in default, the
	// ApplicationSet and its Applications, then their namespaces.
	for _, env := range envs {
		e.DeleteOnCleanup(t, objectRef("v1", "Namespace", "", workloadNS(env)), true)
	}
	for _, env := range envs {
		e.DeleteOnCleanup(t, objectRef("argoproj.io/v1alpha1", "Application", framework.ArgoCDNamespace, app+"-"+env), true)
	}
	e.DeleteOnCleanup(t, objectRef("argoproj.io/v1alpha1", "ApplicationSet", framework.ArgoCDNamespace, app), true)
	t.Cleanup(func() {
		var bundles v1alpha1.BundleList
		if err := e.Client.List(context.Background(), &bundles, sigs_client.InNamespace("default")); err != nil {
			t.Errorf("list the demo's Bundles: %v", err)
			return
		}
		for i := range bundles.Items {
			if b := &bundles.Items[i]; b.Spec.Pipeline == "demo" {
				if err := e.Client.Delete(context.Background(), b); err != nil && !apierrors.IsNotFound(err) {
					t.Errorf("delete Bundle %s: %v", b.Name, err)
				}
			}
		}
	})
	e.DeleteOnCleanup(t, objectRef(v1alpha1.GroupVersion.String(), "Pipeline", "default", "demo"), true)
	e.DeleteOnCleanup(t, objectRef(v1alpha1.GroupVersion.String(), "PolicyGate", "default", "prod-approval"), true)
	e.DeleteOnCleanup(t, objectRef("v1", "Secret", "default", "github-token"), false)

	// Not a local cluster: refused, by name or by server, and nothing is
	// written. The kubeconfig points at this cluster, so a write would land.
	remote := filepath.Join(t.TempDir(), "kubeconfig")
	e.WriteKubeconfig(t, remote, map[string]string{"prod": "", e.Context: ""}, "prod")
	for _, args := range [][]string{
		{"quickstart", "--repo", repo.CloneURL},
		{"quickstart", "--repo", repo.CloneURL, "--context", "prod"},
		{"quickstart", "--delete", "--yes"},
	} {
		r := cli.Exec(framework.CLIOptions{Kubeconfig: remote, Env: withToken.Env}, args...)
		assert.Equal(t, 1, r.Code, r.Output())
		assert.Contains(t, r.Stderr, `the context "prod" (server `+e.Config.Host+`) is not one, and nothing was changed`)
		assert.Contains(t, r.Stderr, "kind create cluster --name kardinal")
		assert.Empty(t, r.Stdout)
	}
	var p v1alpha1.Pipeline
	err = e.Client.Get(ctx, types.NamespacedName{Namespace: "default", Name: "demo"}, &p)
	require.True(t, apierrors.IsNotFound(err), "a refused quickstart writes nothing: %v", err)
	err = e.Client.Get(ctx, types.NamespacedName{Namespace: "default", Name: "github-token"}, &corev1.Secret{})
	require.True(t, apierrors.IsNotFound(err), "a refused quickstart writes nothing: %v", err)

	// A kind context: installs the demo and follows the first version to
	// the approval.
	r := cli.Exec(withToken, "quickstart", "--repo", repo.CloneURL, "--image", image, "--timeout", "10m")
	t.Logf("quickstart:\n%s", r.Output())
	require.Equal(t, 0, r.Code, r.Output())
	assert.NotContains(t, r.Output(), string(token.Data["token"]))
	// The commands it prints name the context it checked and the kubeconfig
	// it read it from (#2233), so they run against this cluster when copied
	// into any shell: the CLI's KUBECONFIG is the kind-only file.
	hint := func(tool string) string { return tool + " --context " + e.Context + " --kubeconfig " + cli.Kubeconfig }
	steps := []string{
		"Setting up the kardinal demo in " + e.Context + "\n",
		"  - kro 0.10.0-rc.0: already installed\n",
		"  - Argo CD v3.5.3: already installed\n",
		"  - kardinal-promoter: already installed (kardinal-system/",
		"  - Secret default/github-token: created from $GITHUB_TOKEN\n",
		"  - Argo CD ApplicationSet argocd/kardinal-test-app (" + repo.CloneURL + "): applied\n",
		"  - PolicyGate default/prod-approval (prod waits for your approval, as kubernetes-admin): applied\n",
		"  - Pipeline default/demo: applied\n",
		"  - First version: shipped as demo-",
		"Following version " + fixtures.V2 + " (demo-",
		") through test, uat and prod (Ctrl-C stops following, the promotion goes on)\n",
		"Version " + fixtures.V2 + " (demo-",
		") stopped: prod waits for approval at gate prod-approval (" + hint("kardinal") + " approve demo prod)\n",
		"\nNext:\n  Approve prod:    " + hint("kardinal") + " approve demo prod\n",
		"  Watch:           " + hint("kardinal") + " status demo -w\n",
		"  Open the UI:     " + hint("kardinal") + " dashboard\n",
		"  Ship the next:   " + hint("kardinal") + " ship demo --image ghcr.io/pnz1990/kardinal-test-app:main@sha256:",
	}
	at := 0
	for _, s := range steps {
		i := strings.Index(r.Stdout[at:], s)
		require.GreaterOrEqual(t, i, 0, "quickstart prints %q after offset %d:\n%s", s, at, r.Stdout)
		at += i + len(s)
	}
	bundle := regexp.MustCompile(`First version: shipped as (\S+)`).FindStringSubmatch(r.Stdout)[1]
	for _, env := range []string{"test", "uat"} {
		e.WaitStepState(t, "default", "demo", bundle, env, "Verified", time.Minute)
		e.WaitDeploymentImage(t, workloadNS(env), app, image, syncTimeout)
	}
	assert.Equal(t, imageV1, e.DeploymentImage(t, workloadNS("prod"), app), "prod waits for the approval")

	var gate v1alpha1.PolicyGate
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: "default", Name: "prod-approval"}, &gate))
	require.NotNil(t, gate.Spec.Approval)
	assert.Equal(t, []string{"kubernetes-admin"}, gate.Spec.Approval.AllowedUsers)
	assert.False(t, gate.Spec.Approval.ExcludeAuthor)

	// why says what prod waits for, and the command that gets past it.
	r = cli.Exec(framework.CLIOptions{}, "why", "demo", "prod")
	t.Logf("why demo prod:\n--- stdout\n%s--- stderr\n%s", r.Stdout, r.Stderr)
	require.Equal(t, 0, r.Code, r.Output())
	assert.Regexp(t, `^prod is waiting for your approval\.\n  Version  `+regexp.QuoteMeta(fixtures.V2)+` \(demo-[a-z0-9]+\), by kubernetes-admin\n`+
		`  Gate     prod-approval · 0 of 1 approvals\n  Next     kardinal approve demo prod\n$`, r.Stdout)
	r = cli.Exec(framework.CLIOptions{}, "why", "demo", "prod", "-o", "wide")
	t.Logf("why demo prod -o wide:\n--- stdout\n%s--- stderr\n%s", r.Stdout, r.Stderr)
	require.Equal(t, 0, r.Code, r.Output())
	assert.Contains(t, r.Stdout, "PolicyGate", "-o wide is explain's table")
	assert.Contains(t, r.Stderr, "kardinal approve demo prod")

	// The command it prints ships it to prod.
	r = cli.Exec(framework.CLIOptions{}, "approve", "demo", "prod", "--yes")
	require.Equal(t, 0, r.Code, r.Output())
	e.WaitStepState(t, "default", "demo", bundle, "prod", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, workloadNS("prod"), app, image, syncTimeout)
	e.WaitBundlePhase(t, "default", bundle, "Verified", time.Minute)
	r = cli.Exec(framework.CLIOptions{}, "status", "demo")
	t.Logf("status demo:\n--- stdout\n%s--- stderr\n%s", r.Stdout, r.Stderr)
	require.Equal(t, 0, r.Code, r.Output())
	for _, env := range envs {
		assert.Regexp(t, `(?m)^\s*`+env+`\s.*`+regexp.QuoteMeta(fixtures.V2), r.Stdout, "status shows %s live on the version", env)
	}

	// A re-run changes nothing and ships nothing.
	r = cli.Exec(withToken, "quickstart", "--repo", repo.CloneURL, "--image", image)
	require.Equal(t, 0, r.Code, r.Output())
	for _, line := range []string{
		"Secret default/github-token: exists; kept\n",
		"ApplicationSet argocd/kardinal-test-app (" + repo.CloneURL + "): already up to date\n",
		"PolicyGate default/prod-approval (prod waits for your approval, as kubernetes-admin): already up to date\n",
		"Pipeline default/demo: already up to date\n",
		"First version: already shipped (" + bundle + ")\n",
	} {
		assert.Contains(t, r.Stdout, line)
	}
	var bundles v1alpha1.BundleList
	require.NoError(t, e.Client.List(ctx, &bundles, sigs_client.InNamespace("default")))
	n := 0
	for _, b := range bundles.Items {
		if b.Spec.Pipeline == "demo" {
			n++
		}
	}
	assert.Equal(t, 1, n, "the re-run ships nothing")

	// What quickstart created, not applied, carries its label: the git
	// Secret and the first version.
	var gitSecret corev1.Secret
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: "default", Name: "github-token"}, &gitSecret))
	assert.Equal(t, "demo", gitSecret.Labels["kardinal.io/quickstart"])
	assert.Equal(t, "demo", getBundle(t, e, "default", bundle).Labels["kardinal.io/quickstart"])

	// --delete removes the demo, and only it: the versions first, then what
	// quickstart applied, then the git Secret.
	r = cli.Exec(framework.CLIOptions{}, "quickstart", "--delete", "--yes")
	t.Logf("quickstart --delete:\n%s", r.Output())
	require.Equal(t, 0, r.Code, r.Output())
	assert.Regexp(t, `(?m)^  \S Versions of default/demo: deleted 1 \(`+regexp.QuoteMeta(fixtures.V2)+` \(`+bundle+`\)\)$`, r.Stdout)
	for _, line := range []string{
		"Pipeline default/demo: deleted\n",
		"PolicyGate default/prod-approval: deleted\n",
		"Argo CD ApplicationSet argocd/kardinal-test-app: deleted (Argo CD deletes its Applications)\n",
		"Secret default/github-token: deleted\n",
		"  " + hint("kubectl") + " delete namespace kardinal-test-app-test kardinal-test-app-uat kardinal-test-app-prod\n",
	} {
		assert.Contains(t, r.Stdout, line)
	}
	for _, obj := range []sigs_client.Object{&v1alpha1.Pipeline{}, &v1alpha1.PolicyGate{}, &corev1.Secret{}, &v1alpha1.Bundle{}} {
		name := map[string]string{"*v1alpha1.Pipeline": "demo", "*v1alpha1.PolicyGate": "prod-approval",
			"*v1.Secret": "github-token", "*v1alpha1.Bundle": bundle}[fmt.Sprintf("%T", obj)]
		err := e.Client.Get(ctx, types.NamespacedName{Namespace: "default", Name: name}, obj)
		assert.True(t, apierrors.IsNotFound(err), "%T %s is deleted: %v", obj, name, err)
	}
	framework.Eventually(t, 2*time.Minute, "Argo CD deletes the ApplicationSet's Applications", func(ctx context.Context) (bool, string) {
		for _, env := range envs {
			a := objectRef("argoproj.io/v1alpha1", "Application", framework.ArgoCDNamespace, app+"-"+env)
			if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.GetNamespace(), Name: a.GetName()}, a); !apierrors.IsNotFound(err) {
				return false, fmt.Sprintf("Application %s-%s: %v", app, env, err)
			}
		}
		return true, ""
	})
	var nsObj corev1.Namespace
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Name: workloadNS("prod")}, &nsObj), "the workload namespaces are kept")
	var demoSteps v1alpha1.PromotionStepList
	require.NoError(t, e.Client.List(ctx, &demoSteps, sigs_client.InNamespace("default"), sigs_client.MatchingLabels{"kardinal.io/pipeline": "demo"}))
	assert.Empty(t, demoSteps.Items, "the versions' steps are gone before the Pipeline")
	var ctrl appsv1.Deployment
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: framework.ControllerNamespace, Name: framework.ControllerDeployment}, &ctrl),
		"kardinal-promoter is kept")

	// A re-run deletes nothing.
	r = cli.Exec(framework.CLIOptions{}, "quickstart", "--delete", "--yes")
	require.Equal(t, 0, r.Code, r.Output())
	for _, what := range []string{"Versions of default/demo", "Pipeline default/demo", "PolicyGate default/prod-approval",
		"Argo CD ApplicationSet argocd/kardinal-test-app", "Secret default/github-token"} {
		assert.Contains(t, r.Stdout, what+": already gone\n")
	}
}
