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

package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// yes is a confirmation that says yes.
func yes(string) error { return nil }

// quickstartDemo sets up the demo on a fake local cluster with quickstart.
func quickstartDemo(t *testing.T, objs ...sigs_client.Object) sigs_client.Client {
	t.Helper()
	writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
	noCluster(t)
	c := quickstartCluster(t, objs...)
	prev := quickstartClient
	t.Cleanup(func() { quickstartClient = prev })
	quickstartClient = func(*clientcmdapi.Config) (sigs_client.Client, error) { return c, nil }
	StubIdentity(t, "kubernetes-admin")
	t.Setenv("GITHUB_TOKEN", "s3cr3t-token")
	o := quickstartOptions{repo: "https://github.com/me/kardinal-demo", image: quickstartImage, noWait: true}
	require.NoError(t, quickstartFn(context.Background(), &bytes.Buffer{}, o))
	return c
}

func gone(t *testing.T, c sigs_client.Client, obj sigs_client.Object, key types.NamespacedName) bool {
	t.Helper()
	err := c.Get(context.Background(), key, obj)
	if err != nil && !apierrors.IsNotFound(err) {
		require.NoError(t, err)
	}
	return apierrors.IsNotFound(err)
}

func appSetObj() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(applicationSetGVK)
	return u
}

func TestQuickstartDelete_RemovesTheDemoOnly(t *testing.T) {
	// Not the demo: another Pipeline's Bundle in default, the controller's
	// token Secret, a Secret of another name.
	other := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "api-1"}, Spec: v1alpha1.BundleSpec{Pipeline: "api"}}
	scm := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: quickstartControllerNS, Name: quickstartSCMSecret}}
	c := quickstartDemo(t, other, scm)
	ctx := context.Background()
	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(ctx, &bundles, sigs_client.InNamespace("default")))
	var demo string
	for _, b := range bundles.Items {
		if b.Spec.Pipeline == "demo" {
			demo = versionLabel(createdOf(&b).Version, b.Name)
		}
	}
	require.NotEmpty(t, demo)

	var out bytes.Buffer
	require.NoError(t, quickstartDeleteFn(ctx, &out, yes, false))
	assert.Equal(t, `Removing the kardinal demo from kind-kardinal
  - Versions of default/demo: deleted 1 (`+demo+`)
  - Pipeline default/demo: deleted
  - PolicyGate default/prod-approval: deleted
  - Argo CD ApplicationSet argocd/kardinal-test-app: deleted (Argo CD deletes its Applications)
  - Secret default/github-token: deleted
Kept: kro, Argo CD, kardinal-promoter and its Secret kardinal-system/kardinal-scm-token, and the namespaces the
Applications deployed to. To remove those too:
  kubectl --context kind-kardinal delete namespace kardinal-test-app-test kardinal-test-app-uat kardinal-test-app-prod
To set the demo up again: kardinal --context kind-kardinal quickstart --repo <your fork of kardinal-demo>
`, withoutKubeconfig(out.String()))

	assert.True(t, gone(t, c, &v1alpha1.Pipeline{}, types.NamespacedName{Namespace: "default", Name: "demo"}))
	assert.True(t, gone(t, c, &v1alpha1.PolicyGate{}, types.NamespacedName{Namespace: "default", Name: "prod-approval"}))
	assert.True(t, gone(t, c, appSetObj(), types.NamespacedName{Namespace: "argocd", Name: "kardinal-test-app"}))
	assert.True(t, gone(t, c, &corev1.Secret{}, types.NamespacedName{Namespace: "default", Name: "github-token"}))
	assert.False(t, gone(t, c, &v1alpha1.Bundle{}, types.NamespacedName{Namespace: "default", Name: "api-1"}), "another Pipeline's Bundle stays")
	assert.False(t, gone(t, c, &corev1.Secret{}, types.NamespacedName{Namespace: quickstartControllerNS, Name: quickstartSCMSecret}),
		"the controller's token stays with the controller")

	// A re-run deletes nothing.
	out.Reset()
	require.NoError(t, quickstartDeleteFn(ctx, &out, yes, false))
	for _, line := range []string{
		"  - Versions of default/demo: already gone\n",
		"  - Pipeline default/demo: already gone\n",
		"  - PolicyGate default/prod-approval: already gone\n",
		"  - Argo CD ApplicationSet argocd/kardinal-test-app: already gone\n",
		"  - Secret default/github-token: already gone\n",
	} {
		assert.Contains(t, out.String(), line)
	}
}

func TestQuickstartDelete_KeepsWhatIsNotQuickstarts(t *testing.T) {
	writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
	noCluster(t)
	// A Pipeline demo, a gate and the Secret someone else made, and a
	// Pipeline that uses the Secret.
	mine := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "demo"},
		Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: "https://github.com/me/x", SecretRef: &v1alpha1.SecretRef{Name: "github-token"}}}}
	user := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "api"},
		Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: "https://github.com/me/api", SecretRef: &v1alpha1.SecretRef{Name: "github-token"}}}}
	b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "demo-1"}, Spec: v1alpha1.BundleSpec{Pipeline: "demo"}}
	gate := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "prod-approval"}}
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "github-token"}}
	c := quickstartCluster(t, mine, user, b, gate, sec)
	prev := quickstartClient
	t.Cleanup(func() { quickstartClient = prev })
	quickstartClient = func(*clientcmdapi.Config) (sigs_client.Client, error) { return c, nil }

	var out bytes.Buffer
	require.NoError(t, quickstartDeleteFn(context.Background(), &out, yes, false))
	for _, line := range []string{
		"  - Pipeline default/demo: kept (quickstart did not create it)\n",
		"  - PolicyGate default/prod-approval: kept (quickstart did not create it)\n",
		"  - Argo CD ApplicationSet argocd/kardinal-test-app: already gone\n",
		"  - Secret default/github-token: kept (quickstart did not create it)\n",
	} {
		assert.Contains(t, out.String(), line)
	}
	assert.NotContains(t, out.String(), "Versions of", "the Bundles of a Pipeline quickstart did not create are not its")
	assert.False(t, gone(t, c, &v1alpha1.Bundle{}, types.NamespacedName{Namespace: "default", Name: "demo-1"}))
	assert.False(t, gone(t, c, &corev1.Secret{}, types.NamespacedName{Namespace: "default", Name: "github-token"}))
}

func TestQuickstartDelete_WaitsForTheVersions(t *testing.T) {
	// A PromotionStep the controller has not let go of (its PR not closed):
	// --delete stops before the Pipeline and the Secret it needs.
	step := &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "demo-1-prod",
		Labels: map[string]string{"kardinal.io/pipeline": "demo"}, Finalizers: []string{"kardinal.io/close-pr"}}}
	c := quickstartDemo(t, step)
	// Its Bundle is going: garbage collection deletes it, the finalizer holds it.
	require.NoError(t, c.Delete(context.Background(), step.DeepCopy()))
	old, oldPoll := quickstartDeleteWait, shipPollInterval
	quickstartDeleteWait, shipPollInterval = 20*time.Millisecond, time.Millisecond
	t.Cleanup(func() { quickstartDeleteWait, shipPollInterval = old, oldPoll })

	err := quickstartDeleteFn(context.Background(), &bytes.Buffer{}, yes, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 PromotionStep(s) still there after 20ms")
	assert.Contains(t, withoutKubeconfig(err.Error()), "Nothing else was deleted. Run kardinal --context kind-kardinal quickstart --delete again")
	assert.False(t, gone(t, c, &v1alpha1.Pipeline{}, types.NamespacedName{Namespace: "default", Name: "demo"}))
	assert.False(t, gone(t, c, &corev1.Secret{}, types.NamespacedName{Namespace: "default", Name: "github-token"}))

	// Once the controller lets go, a re-run finishes.
	var s v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "demo-1-prod"}, &s))
	s.Finalizers = nil
	require.NoError(t, c.Update(context.Background(), &s))
	var out bytes.Buffer
	require.NoError(t, quickstartDeleteFn(context.Background(), &out, yes, false))
	assert.Contains(t, out.String(), "  - Versions of default/demo: already gone\n")
	assert.Contains(t, out.String(), "  - Pipeline default/demo: deleted\n")
}

func TestQuickstartDelete_Refuses(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T)
		confirm func(string) error
		want    error
	}{
		{"not a local cluster", func(t *testing.T) { writeKubeconfig(t, "prod", "https://prod.example.com") }, yes, errNotLocal},
		{"declined", func(t *testing.T) {}, func(string) error { return errCancelled }, errCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := quickstartDemo(t)
			tc.setup(t)
			prevRun := quickstartRun
			t.Cleanup(func() { quickstartRun = prevRun })
			quickstartRun = func(context.Context, string, ...string) (string, error) {
				t.Error("--delete runs no tool")
				return "", nil
			}
			err := quickstartDeleteFn(context.Background(), &bytes.Buffer{}, tc.confirm, false)
			require.ErrorIs(t, err, tc.want)
			assert.False(t, gone(t, c, &v1alpha1.Pipeline{}, types.NamespacedName{Namespace: "default", Name: "demo"}), "nothing was deleted")
		})
	}
}

func TestQuickstartDelete_Flags(t *testing.T) {
	keepStyle(t)
	writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
	noCluster(t)
	for _, args := range [][]string{
		{"quickstart", "--delete", "--repo", "https://github.com/me/kardinal-demo"},
		{"quickstart", "--delete", "--image", "x:1"},
		{"quickstart", "--delete", "--no-wait"},
	} {
		root := NewRootCmd()
		root.SetArgs(args)
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		err := root.Execute()
		require.Error(t, err, strings.Join(args, " "))
		assert.Contains(t, err.Error(), "none of the others can be")
	}
}

// TestQuickstartDelete_OnlyWhatQuickstartMade: a github-token the user made
// before quickstart survives the demo's round trip; with no Pipeline demo, a
// user's Bundle naming it survives and quickstart's own goes; quickstart's
// Secret stays while another Pipeline uses it.
func TestQuickstartDelete_OnlyWhatQuickstartMade(t *testing.T) {
	ctx := context.Background()
	t.Run("the user's Secret", func(t *testing.T) {
		users := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "github-token",
			Labels: map[string]string{v1alpha1.LabelSecretReferenceable: "true"}}}
		c := quickstartDemo(t, users)
		var s corev1.Secret
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "github-token"}, &s))
		assert.Empty(t, s.Labels[quickstartMark], "quickstart kept it, so it does not mark it")
		var out bytes.Buffer
		require.NoError(t, quickstartDeleteFn(ctx, &out, yes, false))
		assert.Contains(t, out.String(), "  - Secret default/github-token: kept (quickstart did not create it)\n")
		assert.Contains(t, out.String(), "  - Pipeline default/demo: deleted\n")
		assert.False(t, gone(t, c, &corev1.Secret{}, types.NamespacedName{Namespace: "default", Name: "github-token"}))
	})
	t.Run("no Pipeline demo", func(t *testing.T) {
		writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
		noCluster(t)
		users := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "demo-mine"}, Spec: v1alpha1.BundleSpec{Pipeline: "demo"}}
		ours := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "demo-qs",
			Labels: map[string]string{quickstartMark: quickstartMarkValue}}, Spec: v1alpha1.BundleSpec{Pipeline: "demo"}}
		c := quickstartCluster(t, users, ours)
		prev := quickstartClient
		t.Cleanup(func() { quickstartClient = prev })
		quickstartClient = func(*clientcmdapi.Config) (sigs_client.Client, error) { return c, nil }
		var out bytes.Buffer
		require.NoError(t, quickstartDeleteFn(ctx, &out, yes, false))
		assert.Contains(t, out.String(), "  - Versions of default/demo: deleted 1 (demo-qs)\n")
		assert.False(t, gone(t, c, &v1alpha1.Bundle{}, types.NamespacedName{Namespace: "default", Name: "demo-mine"}), "the user's Bundle stays")
		assert.True(t, gone(t, c, &v1alpha1.Bundle{}, types.NamespacedName{Namespace: "default", Name: "demo-qs"}))
	})
	t.Run("quickstart's Secret, used by another Pipeline", func(t *testing.T) {
		api := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "api"},
			Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: "https://github.com/me/api", SecretRef: &v1alpha1.SecretRef{Name: "github-token"}}}}
		c := quickstartDemo(t, api)
		var out bytes.Buffer
		require.NoError(t, quickstartDeleteFn(ctx, &out, yes, false))
		assert.Contains(t, out.String(), "  - Secret default/github-token: kept (used by Pipeline api)\n")
		assert.False(t, gone(t, c, &corev1.Secret{}, types.NamespacedName{Namespace: "default", Name: "github-token"}))
	})
}
