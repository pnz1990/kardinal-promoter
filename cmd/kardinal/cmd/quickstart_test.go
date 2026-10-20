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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// writeKubeconfig writes a kubeconfig whose current context is name, with
// server, and points the CLI at it.
func writeKubeconfig(t *testing.T, name, server string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	cfg := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: { server: %q }
contexts:
- name: %q
  context: { cluster: c, user: u }
users:
- name: u
  user: { token: x }
current-context: %q
`, server, name, name)
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0o600))
	oldK, oldC := globalKubeconfig, globalContext
	globalKubeconfig, globalContext = path, ""
	t.Cleanup(func() { globalKubeconfig, globalContext = oldK, oldC })
}

// noCluster makes any cluster or tool access fail the test.
func noCluster(t *testing.T) {
	t.Helper()
	oldB, oldR, oldL := quickstartClient, quickstartRun, quickstartLookPath
	quickstartClient = func(*clientcmdapi.Config) (sigs_client.Client, error) {
		t.Error("quickstart reached a cluster")
		return nil, errors.New("no cluster in this test")
	}
	quickstartRun = func(_ context.Context, name string, args ...string) (string, error) {
		t.Errorf("quickstart ran %s %v", name, args)
		return "", errors.New("no tools in this test")
	}
	quickstartLookPath = func(string) (string, error) { return "/bin/true", nil }
	t.Cleanup(func() { quickstartClient, quickstartRun, quickstartLookPath = oldB, oldR, oldL })
	for _, k := range []string{"HELM_KUBECONTEXT", "HELM_KUBEAPISERVER", "HELM_KUBETOKEN", "KUBERNETES_MASTER",
		"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
}

func TestQuickstart_LocalContextOnly(t *testing.T) {
	cases := []struct {
		name, context, server string
		local                 bool
	}{
		{"kind", "kind-kardinal", "https://127.0.0.1:41234", true},
		{"k3d", "k3d-dev", "https://0.0.0.0:6443", true},
		{"k3d on loopback", "k3d-dev", "https://127.0.0.1:6443", true},
		{"minikube docker", "minikube", "https://192.168.49.2:8443", true},
		{"minikube kvm2", "minikube", "https://192.168.39.10:8443", true},
		{"minikube virtualbox", "minikube", "https://192.168.59.100:8443", true},
		{"minikube, other private network", "minikube", "https://10.0.0.5:8443", false},
		{"minikube, another 192.168 network", "minikube", "https://192.168.1.20:8443", false},
		{"minikube, ULA", "minikube", "https://[fd00::1]:8443", false},
		{"docker desktop", "docker-desktop", "https://kubernetes.docker.internal:6443", true},
		{"rancher desktop", "rancher-desktop", "https://127.0.0.1:6443", true},
		{"ipv6 loopback", "kind-x", "https://[::1]:6443", true},
		{"EKS", "arn:aws:eks:us-east-1:123456789012:cluster/prod", "https://ABCDEF.gr7.us-east-1.eks.amazonaws.com", false},
		{"kind name, remote server", "kind-prod", "https://prod.example.com:6443", false},
		{"local server, other name", "prod", "https://127.0.0.1:6443", false},
		{"kind- prefix only", "kindly-prod", "https://127.0.0.1:6443", false},
		{"public address", "kind-x", "https://203.0.113.7:6443", false},
		{"kind name, private address", "kind-x", "https://10.0.12.7:6443", false},
		{"minikube, public address", "minikube", "https://203.0.113.7:8443", false},
		{"no server", "kind-x", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeKubeconfig(t, tc.context, tc.server)
			got, only, err := requireLocalContext()
			if tc.local {
				require.NoError(t, err)
				assert.Equal(t, tc.context, got)
				require.Len(t, only.Contexts, 1, "only the checked context")
				assert.Equal(t, tc.context, only.CurrentContext)
				return
			}
			require.ErrorIs(t, err, errNotLocal)
			assert.Contains(t, err.Error(), tc.context)
			assert.Contains(t, err.Error(), "nothing was changed")
			assert.Contains(t, err.Error(), "kind create cluster --name kardinal")

			// quickstart itself refuses before it reaches the cluster or
			// runs a tool, whatever the other flags.
			noCluster(t)
			t.Setenv("GITHUB_TOKEN", "tok")
			err = quickstartFn(context.Background(), &bytes.Buffer{}, quickstartOptions{repo: "https://github.com/me/kardinal-demo"})
			require.ErrorIs(t, err, errNotLocal)
		})
	}
}

// TestQuickstart_Kubeconfig: helm and kubectl get a kubeconfig file with
// only the checked context, readable only by the user, with its
// credentials in it.
func TestQuickstart_Kubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
clusters:
- {name: local, cluster: {server: "https://127.0.0.1:6443"}}
- {name: prod, cluster: {server: "https://prod.example.com"}}
contexts:
- {name: kind-kardinal, context: {cluster: local, user: me}}
- {name: prod, context: {cluster: prod, user: admin}}
users:
- {name: me, user: {token: local-token}}
- {name: admin, user: {token: prod-token}}
current-context: prod
`), 0o600))
	oldK, oldC := globalKubeconfig, globalContext
	globalKubeconfig, globalContext = path, "kind-kardinal"
	t.Cleanup(func() { globalKubeconfig, globalContext = oldK, oldC })
	name, only, err := requireLocalContext()
	require.NoError(t, err)
	assert.Equal(t, "kind-kardinal", name)
	file, err := writeHelmKubeconfig(only)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(file) })
	info, err := os.Stat(file)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Contains(t, string(data), "https://127.0.0.1:6443")
	assert.Contains(t, string(data), "current-context: kind-kardinal")
	assert.NotContains(t, string(data), "prod", "no other context, cluster or user")
}

// TestQuickstart_NoTargetOverrides: a variable that points helm, kubectl or
// client-go at another cluster stops quickstart before anything is sent,
// and helm and kubectl run without them, on a kubeconfig with only the
// checked context.
func TestQuickstart_NoTargetOverrides(t *testing.T) {
	for _, k := range []string{"HELM_KUBECONTEXT", "HELM_KUBEAPISERVER", "HELM_KUBETOKEN", "HELM_KUBECAFILE", "HELM_KUBEASUSER", "KUBERNETES_MASTER"} {
		t.Run(k, func(t *testing.T) {
			writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
			noCluster(t)
			t.Setenv("GITHUB_TOKEN", "tok")
			t.Setenv(k, "https://prod.example.com")
			err := quickstartFn(context.Background(), &bytes.Buffer{}, quickstartOptions{repo: "https://github.com/me/kardinal-demo"})
			require.ErrorIs(t, err, errNotLocal)
			assert.Contains(t, err.Error(), "does not run with "+k+" set")
			assert.Contains(t, err.Error(), "unset "+k)
		})
	}
	env := []string{"PATH=/bin", "KUBECONFIG=/home/me/.kube/prod", "HELM_KUBECONTEXT=prod", "HELM_KUBETOKEN=x", "KUBERNETES_MASTER=https://p", "HELM_DEBUG=1"}
	assert.Equal(t, []string{"PATH=/bin", "HELM_DEBUG=1"}, scrubbedEnv(env))
	assert.Equal(t, []string{"HELM_KUBECONTEXT", "HELM_KUBETOKEN", "KUBERNETES_MASTER"}, targetOverrides(env))
}

func TestQuickstart_Preconditions(t *testing.T) {
	cases := []struct {
		name   string
		repo   string
		token  string
		noTool string
		want   string
	}{
		{"no repo", "", "tok", "", "quickstart needs --local (a git server in the cluster, no account needed) or --repo"},
		{"no helm", "https://github.com/me/kardinal-demo", "tok", "helm", "quickstart needs helm on PATH"},
		{"no kubectl", "https://github.com/me/kardinal-demo", "tok", "kubectl", "quickstart needs kubectl on PATH"},
		{"no token", "https://github.com/me/kardinal-demo", "", "", "needs a GitHub token with contents and pull requests read and write on https://github.com/me/kardinal-demo: export GITHUB_TOKEN=<token>, or log in with gh auth login"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
			noCluster(t)
			setForTest(t, &quickstartLookPath, func(name string) (string, error) {
				if name == tc.noTool {
					return "", errors.New("not found")
				}
				return "/bin/" + name, nil
			})
			t.Setenv("GITHUB_TOKEN", tc.token)
			oldGH := ghToken
			ghToken = func() (string, error) { return "", errors.New("not logged in") }
			t.Cleanup(func() { ghToken = oldGH })
			err := quickstartFn(context.Background(), &bytes.Buffer{}, quickstartOptions{repo: tc.repo})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestQuickstart_Versions checks that quickstart installs what the e2e
// environment installs: hack/install-kro.sh's kro and chart values, and
// hack/e2e/versions.env's Argo CD.
func TestQuickstart_Versions(t *testing.T) {
	kro, err := os.ReadFile("../../../hack/install-kro.sh")
	require.NoError(t, err)
	def := func(name string) string {
		m := regexp.MustCompile(name + `="\$\{` + name + `:-([^}]*)\}"`).FindSubmatch(kro)
		require.NotNil(t, m, name)
		return string(m[1])
	}
	assert.Equal(t, def("KRO_VERSION"), quickstartKroVersion)
	assert.Equal(t, def("KRO_CHART"), kroChart)
	assert.Equal(t, def("KRO_NAMESPACE"), kroNamespace)
	assert.Equal(t, []string{
		"config.featureGates.GraphKind=true",
		"rbac.mode=" + def("KRO_RBAC_MODE"),
		"config.graphConcurrentReconciles=" + def("KRO_GRAPH_CONCURRENT_RECONCILES"),
		"config.clientQps=" + def("KRO_CLIENT_QPS"),
		"config.clientBurst=" + def("KRO_CLIENT_BURST"),
		"deployment.resources.limits.memory=" + def("KRO_MEMORY_LIMIT"),
		"deployment.resources.requests.memory=" + def("KRO_MEMORY_REQUEST"),
	}, kroHelmValues)

	env, err := os.ReadFile("../../../hack/e2e/versions.env")
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^ARGOCD_RELEASE="?([^"\s]+)"?`).FindSubmatch(env)
	require.NotNil(t, m)
	assert.Equal(t, string(m[1]), argoCDRelease)

	// The images are real kardinal-test-app tags, pinned by digest; the
	// command quickstart prints ships the one after the first.
	for _, img := range quickstartImages {
		assert.Regexp(t, `^ghcr\.io/pnz1990/kardinal-test-app:[a-z0-9.-]+@sha256:[0-9a-f]{64}$`, img)
	}
	assert.Equal(t, quickstartImages[0], quickstartImage)
	assert.Equal(t, quickstartImages[1], nextImage(quickstartImages[0]))
	assert.Equal(t, quickstartImages[len(quickstartImages)-1], nextImage(quickstartImages[len(quickstartImages)-1]))
	assert.Equal(t, quickstartImages[len(quickstartImages)-1], nextImage("ghcr.io/other/app:1.0"), "a --image quickstart does not know")
}

// quickstartCluster is a fake cluster where kro, Argo CD and kardinal run:
// server-side apply is create or update, and a Pipeline is Ready as soon as
// it is written.
func quickstartCluster(t *testing.T, objs ...sigs_client.Object) sigs_client.Client {
	t.Helper()
	mapper := meta.NewDefaultRESTMapper(nil)
	for _, gvk := range []schema.GroupVersionKind{
		{Group: "kro.run", Version: "v1alpha1", Kind: "Graph"},
		applicationSetGVK,
		applicationGVK,
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "AppProject"},
		v1alpha1.GroupVersion.WithKind("Pipeline"),
		v1alpha1.GroupVersion.WithKind("PolicyGate"),
		v1alpha1.GroupVersion.WithKind("Bundle"),
		v1alpha1.GroupVersion.WithKind("PromotionStep"),
		corev1.SchemeGroupVersion.WithKind("Secret"),
		corev1.SchemeGroupVersion.WithKind("Namespace"),
		corev1.SchemeGroupVersion.WithKind("ConfigMap"),
		corev1.SchemeGroupVersion.WithKind("Service"),
		corev1.SchemeGroupVersion.WithKind("PersistentVolumeClaim"),
		appsv1.SchemeGroupVersion.WithKind("Deployment"),
	} {
		scope := meta.RESTScopeNamespace
		if gvk.Kind == "Namespace" {
			scope = meta.RESTScopeRoot
		}
		mapper.Add(gvk, scope)
	}
	ready := func(name string) *appsv1.Deployment {
		ns, n, _ := strings.Cut(name, "/")
		d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: n,
			Labels: map[string]string{"app.kubernetes.io/name": n}}}
		d.Status.ReadyReplicas = 1
		return d
	}
	all := append([]sigs_client.Object{ready("kro-system/kro"), ready("argocd/argocd-repo-server"),
		ready("kardinal-system/kardinal-promoter")}, objs...)
	// applied are the objects quickstart applied: Get gives them the field
	// manager the API server records (the fake client keeps none).
	applied := map[string]bool{}
	c := fake.NewClientBuilder().WithScheme(rootScheme).WithRESTMapper(mapper).WithObjects(all...).
		WithStatusSubresource(&appsv1.Deployment{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl sigs_client.WithWatch, key sigs_client.ObjectKey, obj sigs_client.Object, opts ...sigs_client.GetOption) error {
				if err := cl.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				gvk, err := apiutil.GVKForObject(obj, cl.Scheme())
				if err == nil && applied[gvk.Kind+"/"+key.Namespace+"/"+key.Name] {
					obj.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: quickstartFieldManager, Operation: metav1.ManagedFieldsOperationApply}})
				}
				return nil
			},
			Delete: func(ctx context.Context, cl sigs_client.WithWatch, obj sigs_client.Object, opts ...sigs_client.DeleteOption) error {
				if err := cl.Delete(ctx, obj, opts...); err != nil {
					return err
				}
				if gvk, err := apiutil.GVKForObject(obj, cl.Scheme()); err == nil {
					delete(applied, gvk.Kind+"/"+obj.GetNamespace()+"/"+obj.GetName())
				}
				return nil
			},
			Apply: func(ctx context.Context, cl sigs_client.WithWatch, obj runtime.ApplyConfiguration, _ ...sigs_client.ApplyOption) error {
				content := obj.(interface{ UnstructuredContent() map[string]any }).UnstructuredContent()
				u := &unstructured.Unstructured{Object: runtime.DeepCopyJSON(content)}
				if u.GetKind() == "Pipeline" {
					_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{
						"type": "Ready", "status": "True", "reason": "Valid", "message": "",
						"lastTransitionTime": "2026-01-01T00:00:00Z"}}, "status", "conditions")
				}
				applied[u.GroupVersionKind().Kind+"/"+u.GetNamespace()+"/"+u.GetName()] = true
				cur := &unstructured.Unstructured{}
				cur.SetGroupVersionKind(u.GroupVersionKind())
				err := cl.Get(ctx, sigs_client.ObjectKeyFromObject(u), cur)
				if apierrors.IsNotFound(err) {
					return cl.Create(ctx, u)
				}
				if err != nil {
					return err
				}
				if sameJSON(cur.Object["spec"], typedSpec(t, u)) && sameJSON(cur.GetLabels(), u.GetLabels()) {
					return nil
				}
				u.SetResourceVersion(cur.GetResourceVersion())
				return cl.Update(ctx, u)
			},
		}).Build()
	return c
}

// typedSpec is u's spec as the API server stores it: through its Go type,
// when the scheme has one.
func typedSpec(t *testing.T, u *unstructured.Unstructured) any {
	t.Helper()
	obj, err := rootScheme.New(u.GroupVersionKind())
	if err != nil {
		return u.Object["spec"]
	}
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, obj))
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	require.NoError(t, err)
	return m["spec"]
}

// sameJSON reports whether a and b are the same JSON (an int64 and a
// float64 of the same value are).
func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

func TestQuickstart_Demo(t *testing.T) {
	writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
	noCluster(t)
	c := quickstartCluster(t)
	setForTest(t, &quickstartClient, func(*clientcmdapi.Config) (sigs_client.Client, error) { return c, nil })
	StubIdentity(t, "kubernetes-admin")
	t.Setenv("GITHUB_TOKEN", "s3cr3t-token")
	o := quickstartOptions{repo: "https://github.com/me/kardinal-demo", image: quickstartImage, noWait: true}

	var out bytes.Buffer
	require.NoError(t, quickstartFn(context.Background(), &out, o))
	got := out.String()
	assert.NotContains(t, got, "s3cr3t-token")
	assert.Equal(t, `Setting up the kardinal demo in kind-kardinal
  - kro 0.10.0-rc.0: already installed
  - Argo CD v3.5.3: already installed
  - kardinal-promoter: already installed (kardinal-system/kardinal-promoter)
  - Secret default/github-token: created from $GITHUB_TOKEN
  - Argo CD ApplicationSet argocd/kardinal-test-app (https://github.com/me/kardinal-demo): applied
  - PolicyGate default/prod-approval (prod waits for your approval, as kubernetes-admin): applied
  - Pipeline default/demo: applied
`, strings.SplitAfter(got, "  - Pipeline default/demo: applied\n")[0])
	assert.Regexp(t, `  - First version: shipped as demo-\S+\n`, got)
	// The commands it prints name the context, and the --kubeconfig it was given.
	assert.Contains(t, got, "Approve prod:    kardinal --context kind-kardinal --kubeconfig "+globalKubeconfig+" approve demo prod\n")
	got = withoutKubeconfig(got)
	assert.Contains(t, got, "Approve prod:    kardinal --context kind-kardinal approve demo prod\n")
	assert.Contains(t, got, "Open the UI:     kardinal --context kind-kardinal dashboard\n")
	assert.Contains(t, got, "Ship the next:   kardinal --context kind-kardinal ship demo --image "+quickstartImages[1]+" --wait\n")

	ctx := context.Background()
	var s corev1.Secret
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "github-token"}, &s))
	assert.Equal(t, "true", s.Labels[v1alpha1.LabelSecretReferenceable])

	var g v1alpha1.PolicyGate
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "prod-approval"}, &g))
	assert.Equal(t, "prod", g.Labels["kardinal.io/applies-to"])
	require.NotNil(t, g.Spec.Approval)
	assert.Equal(t, []string{"kubernetes-admin"}, g.Spec.Approval.AllowedUsers)
	assert.False(t, g.Spec.Approval.ExcludeAuthor)

	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "demo"}, &p))
	assert.Equal(t, o.repo, p.Spec.Git.URL)
	require.Len(t, p.Spec.Environments, 3)
	for i, env := range []string{"test", "uat", "prod"} {
		assert.Equal(t, env, p.Spec.Environments[i].Name)
		assert.Equal(t, "argocd", p.Spec.Environments[i].Health.Type)
		assert.Equal(t, "kardinal-test-app-"+env, p.Spec.Environments[i].Health.ArgoCD.Name)
	}

	appSet := &unstructured.Unstructured{}
	appSet.SetGroupVersionKind(applicationSetGVK)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "argocd", Name: "kardinal-test-app"}, appSet))
	repo, _, _ := unstructured.NestedString(appSet.Object, "spec", "template", "spec", "source", "repoURL")
	assert.Equal(t, o.repo, repo)

	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(ctx, &bundles, sigs_client.InNamespace("default")))
	require.Len(t, bundles.Items, 1)
	assert.Equal(t, "version sha-9349a3f ("+bundles.Items[0].Name+")", versionRef(createdOf(&bundles.Items[0]).Version, bundles.Items[0].Name),
		"quickstart's output names the version by its tag")

	// A re-run changes nothing and ships nothing.
	out.Reset()
	require.NoError(t, quickstartFn(context.Background(), &out, o))
	again := out.String()
	for _, line := range []string{
		"Secret default/github-token: exists; kept",
		"ApplicationSet argocd/kardinal-test-app (https://github.com/me/kardinal-demo): already up to date",
		"PolicyGate default/prod-approval (prod waits for your approval, as kubernetes-admin): already up to date",
		"Pipeline default/demo: already up to date",
		"First version: already shipped (" + bundles.Items[0].Name + ")",
	} {
		assert.Contains(t, again, line)
	}
	require.NoError(t, c.List(ctx, &bundles, sigs_client.InNamespace("default")))
	assert.Len(t, bundles.Items, 1)
}

func TestQuickstart_Installs(t *testing.T) {
	writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
	noCluster(t)
	t.Setenv("GITHUB_TOKEN", "s3cr3t-token")
	// Nothing runs yet: every install step runs its commands.
	c := fake.NewClientBuilder().WithScheme(rootScheme).Build()
	var ran []string
	setForTest(t, &quickstartRun, func(_ context.Context, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		assert.NotContains(t, line, "s3cr3t-token")
		ran = append(ran, line)
		return "", nil
	})
	q := &quickstart{ctx: context.Background(), w: &bytes.Buffer{}, c: c, kctx: "kind-kardinal", kubeconfig: "/tmp/qs-kubeconfig",
		o: quickstartOptions{chartVersion: "9.9.9", sets: []string{"ui.enabled=true"}}}
	require.NoError(t, q.kro())
	require.NoError(t, q.argoCD())
	require.NoError(t, q.kardinal())

	all := strings.Join(ran, "\n")
	assert.Contains(t, all, "helm upgrade --install kro oci://registry.k8s.io/kro/charts/kro --version 0.10.0-rc.0 --namespace kro-system --create-namespace")
	assert.Contains(t, all, "--set config.featureGates.GraphKind=true")
	assert.Contains(t, all, "https://raw.githubusercontent.com/kubernetes-sigs/kro/v0.10.0-rc.0/helm/crds/kro.run_graphs.yaml")
	assert.Contains(t, all, "https://raw.githubusercontent.com/argoproj/argo-cd/v3.5.3/manifests/install.yaml")
	assert.Contains(t, all, "helm upgrade --install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --namespace kardinal-system --create-namespace "+
		"--set scm.provider=github --set scm.secretRef.name=kardinal-scm-token --wait --timeout 300s --version 9.9.9 --set ui.enabled=true")
	for _, line := range ran {
		if strings.HasPrefix(line, "helm ") {
			assert.Contains(t, line, "--kubeconfig /tmp/qs-kubeconfig --kube-context kind-kardinal", line)
		} else {
			assert.Contains(t, line, "--kubeconfig /tmp/qs-kubeconfig --context kind-kardinal", line)
		}
	}

	// The Argo CD it installs polls git every 10s, set before the install.
	var cm corev1.ConfigMap
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "argocd", Name: "argocd-cm"}, &cm))
	assert.Equal(t, map[string]string{"timeout.reconciliation": "10s", "timeout.reconciliation.jitter": "0s"}, cm.Data)

	// The controller's SCM Secret: the token, and no referenceable label.
	var s corev1.Secret
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "kardinal-system", Name: quickstartSCMSecret}, &s))
	assert.Equal(t, "s3cr3t-token", s.StringData["token"])
	assert.Empty(t, s.Labels[v1alpha1.LabelSecretReferenceable])
}

// TestQuickstart_NoProxy: a context whose cluster has a proxy-url is
// refused before anything is sent: the proxy decides where requests go.
func TestQuickstart_NoProxy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
clusters:
- {name: local, cluster: {server: "https://127.0.0.1:6443", proxy-url: "socks5://127.0.0.1:1080"}}
contexts:
- {name: kind-kardinal, context: {cluster: local, user: me}}
users:
- {name: me, user: {token: t}}
current-context: kind-kardinal
`), 0o600))
	oldK, oldC := globalKubeconfig, globalContext
	globalKubeconfig, globalContext = path, ""
	t.Cleanup(func() { globalKubeconfig, globalContext = oldK, oldC })
	noCluster(t)
	t.Setenv("GITHUB_TOKEN", "tok")
	err := quickstartFn(context.Background(), &bytes.Buffer{}, quickstartOptions{repo: "https://github.com/me/kardinal-demo"})
	require.ErrorIs(t, err, errNotLocal)
	assert.Contains(t, err.Error(), "through socks5://127.0.0.1:1080")
	assert.Contains(t, err.Error(), "nothing was changed")
}

// TestQuickstart_NoEnvironmentProxy: a proxy of the environment that applies
// to the local server (Go bypasses it only for localhost and 127.0.0.1) is
// refused before anything is sent, with the variable and the NO_PROXY that
// excludes the server; NO_PROXY naming the server lets it pass.
func TestQuickstart_NoEnvironmentProxy(t *testing.T) {
	cases := []struct {
		name, context, server, variable, noProxy string
		refused                                  bool
	}{
		{"k3d on 0.0.0.0", "k3d-demo", "https://0.0.0.0:6443", "HTTPS_PROXY", "", true},
		{"docker desktop host", "docker-desktop", "https://host.docker.internal:6443", "HTTPS_PROXY", "", true},
		{"minikube network", "minikube", "https://192.168.49.2:8443", "https_proxy", "", true},
		{"http server and HTTP_PROXY", "kind-kardinal", "http://0.0.0.0:8080", "HTTP_PROXY", "", true},
		{"NO_PROXY names other hosts", "k3d-demo", "https://0.0.0.0:6443", "HTTPS_PROXY", ".corp.example", true},
		{"NO_PROXY names the host", "minikube", "https://192.168.49.2:8443", "HTTPS_PROXY", "192.168.49.2", false},
		{"NO_PROXY names the network", "minikube", "https://192.168.49.2:8443", "HTTPS_PROXY", "192.168.49.0/24", false},
		{"loopback is never proxied", "kind-kardinal", "https://127.0.0.1:41234", "HTTPS_PROXY", "", false},
		{"HTTP_PROXY does not apply to https", "k3d-demo", "https://0.0.0.0:6443", "HTTP_PROXY", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeKubeconfig(t, tc.context, tc.server)
			noCluster(t)
			t.Setenv(tc.variable, "http://user:secret@proxy.corp.example:3128")
			t.Setenv("NO_PROXY", tc.noProxy)
			_, _, err := requireLocalContext()
			if !tc.refused {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, errNotLocal)
			host := tc.server[strings.Index(tc.server, "//")+2 : strings.LastIndex(tc.server, ":")]
			assert.Contains(t, err.Error(), tc.variable+"=http://user:xxxxx@proxy.corp.example:3128 applies to the server "+tc.server)
			want := host
			if tc.noProxy != "" {
				want = tc.noProxy + "," + host // the NO_PROXY set is kept
			}
			assert.Contains(t, err.Error(), "export NO_PROXY="+want+" (or unset "+tc.variable+")")
			assert.Contains(t, err.Error(), "nothing was changed")
			assert.NotContains(t, err.Error(), "secret", "the proxy password is not printed")
		})
	}
	// The credentials are masked in the proxy Go parsed, also for a value
	// without a scheme and for a username alone (a token).
	for value, want := range map[string]string{
		"user:p4ss@proxy.corp:3128": "HTTPS_PROXY=http://user:xxxxx@proxy.corp:3128 applies",
		"http://tok3n@proxy":        "HTTPS_PROXY=http://xxxxx@proxy applies",
	} {
		writeKubeconfig(t, "k3d-demo", "https://0.0.0.0:6443")
		noCluster(t)
		t.Setenv("HTTPS_PROXY", value)
		_, _, err := requireLocalContext()
		require.ErrorIs(t, err, errNotLocal, value)
		assert.Contains(t, err.Error(), want)
		assert.NotContains(t, err.Error(), "p4ss")
		assert.NotContains(t, err.Error(), "tok3n")
	}

	// quickstartFn stops there too, before any cluster call or tool.
	writeKubeconfig(t, "k3d-demo", "https://0.0.0.0:6443")
	noCluster(t)
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("HTTPS_PROXY", "http://proxy.corp.example:3128")
	err := quickstartFn(context.Background(), &bytes.Buffer{}, quickstartOptions{repo: "https://github.com/me/kardinal-demo"})
	require.ErrorIs(t, err, errNotLocal)
}

// TestQuickstart_InterruptRemovesKubeconfig: when the run is cancelled
// (Ctrl-C, SIGTERM) in the middle of an install, quickstart returns and its
// kubeconfig file for helm and kubectl is gone; KUBERC is off for them.
func TestQuickstart_InterruptRemovesKubeconfig(t *testing.T) {
	writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
	noCluster(t)
	t.Setenv("GITHUB_TOKEN", "tok")
	c := fake.NewClientBuilder().WithScheme(rootScheme).Build() // nothing installed
	setForTest(t, &quickstartClient, func(*clientcmdapi.Config) (sigs_client.Client, error) { return c, nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var file string
	setForTest(t, &quickstartRun, func(ctx context.Context, name string, args ...string) (string, error) {
		for i, a := range args {
			if a == "--kubeconfig" {
				file = args[i+1]
			}
		}
		require.FileExists(t, file, "helm and kubectl get the kubeconfig file")
		cancel() // Ctrl-C while helm runs
		return "", ctx.Err()
	})
	err := quickstartFn(ctx, &bytes.Buffer{}, quickstartOptions{repo: "https://github.com/me/kardinal-demo"})
	require.Error(t, err)
	require.NotEmpty(t, file)
	assert.NoFileExists(t, file, "the kubeconfig file is removed")

	assert.Equal(t, []string{"PATH=/bin", "KUBERC=off", "KUBECONFIG=" + os.DevNull},
		toolEnv([]string{"KUBERC=/home/me/.kube/kuberc", "PATH=/bin", "HELM_KUBECONTEXT=prod", "KUBECONFIG=/home/me/.kube/config"}))
}
