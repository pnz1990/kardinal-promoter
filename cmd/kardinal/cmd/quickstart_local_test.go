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
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// localTestTokens are the tokens the fake git server hands out, in order.
var localTestTokens = []string{strings.Repeat("a1", 20), strings.Repeat("b2", 20), strings.Repeat("c3", 20)}

// defaultClass is a default StorageClass, as kind has.
func defaultClass() *storagev1.StorageClass {
	return &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "standard",
		Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}}, Provisioner: "rancher.io/local-path"}
}

// localTestCRDs is what kubectl get crd -o json prints: a CRD of each group
// --local --delete looks at.
const localTestCRDs = `{"items":[
 {"metadata":{"name":"pipelines.kardinal.io"},"spec":{"group":"kardinal.io","names":{"kind":"Pipeline","listKind":"PipelineList"},"versions":[{"name":"v1alpha1","storage":true}]}},
 {"metadata":{"name":"bundles.kardinal.io"},"spec":{"group":"kardinal.io","names":{"kind":"Bundle","listKind":"BundleList"},"versions":[{"name":"v1alpha1","storage":true}]}},
 {"metadata":{"name":"applications.argoproj.io"},"spec":{"group":"argoproj.io","names":{"kind":"Application","listKind":"ApplicationList"},"versions":[{"name":"v1alpha1","storage":true}]}},
 {"metadata":{"name":"applicationsets.argoproj.io"},"spec":{"group":"argoproj.io","names":{"kind":"ApplicationSet","listKind":"ApplicationSetList"},"versions":[{"name":"v1alpha1","storage":true}]}},
 {"metadata":{"name":"graphs.kro.run"},"spec":{"group":"kro.run","names":{"kind":"Graph","listKind":"GraphList"},"versions":[{"name":"v1alpha1","storage":true}]}}
]}`

// fakeTools answers quickstart's helm and kubectl commands as a cluster
// with a git server would, recording each line. A token never appears in a
// command line (asserted for every line); helmValues is what helm get
// values prints for the installed release ("" when there is none).
type fakeTools struct {
	t          *testing.T
	ran        []string
	tokens     int
	helmValues string
}

func (f *fakeTools) run(_ context.Context, name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	for _, tok := range localTestTokens {
		assert.NotContains(f.t, line, tok, "a token in a command line")
	}
	f.ran = append(f.ran, line)
	// Every command that reaches the cluster names quickstart's kubeconfig
	// and context before any "--" (after it, kubectl exec passes them to the
	// command and falls back to the default kubeconfig context).
	if line != "helm version --short" {
		head, _, _ := strings.Cut(line, " -- ")
		assert.Contains(f.t, head, " --kubeconfig ", "the kubeconfig flag before --: %s", line)
		assert.Regexp(f.t, ` --(kube-)?context `, head, "the context flag before --: %s", line)
	}
	switch {
	// The seed script generates its own token inside the container; its
	// output never has one.
	case strings.Contains(line, " exec ") && strings.Contains(line, " -- sh -c "):
		return "repository seeded\n", nil
	case strings.Contains(line, " exec ") && strings.Contains(line, "generate-access-token"):
		tok := localTestTokens[f.tokens%len(localTestTokens)]
		f.tokens++
		// What kubectl exec prints: a log line of forgejo's, then the token.
		return "2026/10/19 12:00:00 ...cmd/admin.go:42:runCreateUser() [I] a log line\n" + tok + "\n", nil
	case line == "helm version --short":
		return "v3.22.0+g144ca65\n", nil
	case strings.HasPrefix(line, "helm get values"):
		if f.helmValues == "" {
			return "", errors.New("release: not found")
		}
		return f.helmValues, nil
	case strings.HasPrefix(line, "kubectl get crd -o json"):
		return localTestCRDs, nil
	}
	return "", nil
}

// installs are the commands of f that install or uninstall something.
func (f *fakeTools) installs() []string {
	var out []string
	for _, l := range f.ran {
		if strings.HasPrefix(l, "helm upgrade") || strings.HasPrefix(l, "helm uninstall") || strings.Contains(l, " exec ") ||
			strings.HasPrefix(l, "kubectl apply") || strings.HasPrefix(l, "kubectl delete") || strings.HasPrefix(l, "kubectl create") {
			out = append(out, l)
		}
	}
	return out
}

// localHelmValuesJSON is what helm get values prints for a release
// quickstart --local installed.
const localHelmValuesJSON = `{"scm":{"apiURL":"` + localGitBase + `","provider":"forgejo","secretRef":{"name":"kardinal-scm-token"}}}`

// localDemo runs quickstart --local on a fake local cluster where kro, Argo
// CD and a kardinal-promoter for the local git server run.
// withoutKubeconfig is s without the --kubeconfig flag of the test's
// kubeconfig, which the commands quickstart prints carry after --context.
func withoutKubeconfig(s string) string {
	return strings.ReplaceAll(s, " --kubeconfig "+globalKubeconfig, "")
}

func localDemo(t *testing.T, objs ...sigs_client.Object) (sigs_client.Client, *fakeTools, string) {
	t.Helper()
	writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
	noCluster(t)
	c := quickstartCluster(t, append([]sigs_client.Object{defaultClass()}, objs...)...)
	setForTest(t, &quickstartClient, func(*clientcmdapi.Config) (sigs_client.Client, error) { return c, nil })
	tools := &fakeTools{t: t, helmValues: localHelmValuesJSON}
	setForTest(t, &quickstartRun, tools.run)
	StubIdentity(t, "kubernetes-admin")
	// No GitHub token anywhere: --local needs none.
	t.Setenv("GITHUB_TOKEN", "")
	setForTest(t, &ghToken, func() (string, error) { return "", errors.New("not logged in") })
	var out bytes.Buffer
	require.NoError(t, quickstartFn(context.Background(), &out, quickstartOptions{local: true, image: quickstartImage, noWait: true}))
	return c, tools, withoutKubeconfig(out.String())
}

// installID is the install ID of the cluster's quickstart --local.
func installID(t *testing.T, c sigs_client.Client) string {
	t.Helper()
	var cm corev1.ConfigMap
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: localGitNS, Name: quickstartRecord}, &cm))
	require.Regexp(t, `^[0-9a-f]{12}$`, cm.Data["id"])
	return cm.Data["id"]
}

// TestQuickstartLocal_Demo: quickstart --local sets the demo up on the git
// server it installs, with no GitHub token, marks what it creates with its
// install ID, and a re-run changes nothing.
//
// Covers QUICKSTART-LOCAL-03.
func TestQuickstartLocal_Demo(t *testing.T) {
	c, tools, got := localDemo(t)
	ctx := context.Background()
	for _, tok := range localTestTokens {
		assert.NotContains(t, got, tok, "a token in quickstart's output")
	}
	assert.Equal(t, `Setting up the kardinal demo in kind-kardinal
  - kro 0.10.0-rc.0: already installed
  - Argo CD v3.5.3: already installed
  - Git server kardinal-git/kardinal-git (`+localRepoURL+`): installed, repository kardinal/kardinal-demo seeded
  - kardinal-promoter: already installed (kardinal-system/kardinal-promoter)
  - Secret default/git-token: created (the Pipeline's token on the git server)
  - Argo CD ApplicationSet argocd/kardinal-test-app (`+localRepoURL+`): applied
  - PolicyGate default/prod-approval (prod waits for your approval, as kubernetes-admin): applied
  - Pipeline default/demo: applied
`, strings.SplitAfter(got, "  - Pipeline default/demo: applied\n")[0])
	assert.Contains(t, got, "  See the repo:    kubectl --context kind-kardinal -n kardinal-git port-forward svc/kardinal-git 3000, then http://127.0.0.1:3000/kardinal/kardinal-demo\n")
	assert.Contains(t, got, "  Remove it all:   kardinal --context kind-kardinal quickstart --local --delete\n")

	// The git server: seeded by kubectl exec of a script with no secret in it.
	all := strings.Join(tools.ran, "\n")
	assert.Contains(t, all, "kubectl -n kardinal-git rollout status deployment/kardinal-git --timeout=300s")
	assert.Regexp(t, `kubectl --kubeconfig \S+ --context kind-kardinal -n kardinal-git exec deployment/kardinal-git -c forgejo -- sh -c `, all)
	assert.Contains(t, all, " -- sh -c "+localGitSeedScript)

	// The Pipeline's git Secret holds the server's token, referenceable;
	// the Pipeline and the ApplicationSet use the server's repository.
	id := installID(t, c)
	var s corev1.Secret
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: localGitSecret}, &s))
	assert.Equal(t, localTestTokens[0], s.StringData["token"])
	assert.Equal(t, "true", s.Labels[v1alpha1.LabelSecretReferenceable])
	assert.Equal(t, id, s.Labels[quickstartIDLabel])
	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "demo"}, &p))
	assert.Equal(t, localRepoURL, p.Spec.Git.URL)
	require.NotNil(t, p.Spec.Git.SecretRef)
	assert.Equal(t, localGitSecret, p.Spec.Git.SecretRef.Name)
	appSet := &unstructured.Unstructured{}
	appSet.SetGroupVersionKind(applicationSetGVK)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "argocd", Name: quickstartAppSet}, appSet))
	repo, _, _ := unstructured.NestedString(appSet.Object, "spec", "template", "spec", "source", "repoURL")
	assert.Equal(t, localRepoURL, repo)
	// The ApplicationSet and its Applications carry the install ID, by which
	// --local --delete knows them.
	assert.Equal(t, id, appSet.GetLabels()[quickstartIDLabel])
	appLabels, _, _ := unstructured.NestedStringMap(appSet.Object, "spec", "template", "metadata", "labels")
	assert.Equal(t, map[string]string{quickstartIDLabel: id}, appLabels)

	// What it created carries the install ID: the git server's namespace and
	// the Applications' namespaces (absent before).
	for _, name := range append(quickstartWorkloadNamespaces(), localGitNS) {
		var ns corev1.Namespace
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name}, &ns))
		assert.Equal(t, id, ns.Labels[quickstartIDLabel], name)
	}

	// A re-run changes nothing: the same ID, no new token, nothing re-created.
	tokens := tools.tokens
	tools.ran = nil
	var out bytes.Buffer
	require.NoError(t, quickstartFn(ctx, &out, quickstartOptions{local: true, image: quickstartImage, noWait: true}))
	assert.Equal(t, tokens, tools.tokens, "a re-run creates no token")
	assert.Equal(t, id, installID(t, c))
	assert.Contains(t, out.String(), "Secret default/git-token: kept")
	assert.Contains(t, out.String(), "First version: already shipped")
}

// TestQuickstartLocal_ServerNotExposed: the git server is reachable inside
// the cluster only (a ClusterIP Service, no NodePort, hostPort or host
// network), runs unprivileged, and none of its objects holds a credential.
//
// Covers QUICKSTART-LOCAL-03.
func TestQuickstartLocal_ServerNotExposed(t *testing.T) {
	t.Parallel()
	objs, err := localGitObjects()
	require.NoError(t, err)
	kinds := map[string]*unstructured.Unstructured{}
	for _, u := range objs {
		kinds[u.GetKind()] = u
		assert.Equal(t, localGitNS, u.GetNamespace())
		assert.Equal(t, quickstartMarkValue, u.GetLabels()[quickstartMark])
		b, err := u.MarshalJSON()
		require.NoError(t, err)
		assert.NotRegexp(t, `(?i)password|secretKeyRef|envFrom|nodePort|hostPort|hostNetwork|hostPath|LoadBalancer|privileged|(^|[^0-9a-f])[0-9a-f]{40}([^0-9a-f]|$)`,
			string(b), u.GetKind())
	}
	require.Len(t, kinds, 3)
	svc := kinds["Service"]
	typ, _, _ := unstructured.NestedString(svc.Object, "spec", "type")
	assert.Equal(t, "ClusterIP", typ)
	pod, _, _ := unstructured.NestedMap(kinds["Deployment"].Object, "spec", "template", "spec")
	assert.Equal(t, false, pod["automountServiceAccountToken"], "the server needs no Kubernetes API access")
	sc := pod["securityContext"].(map[string]any)
	assert.Equal(t, true, sc["runAsNonRoot"])
	ctr := pod["containers"].([]any)[0].(map[string]any)
	assert.Equal(t, localGitImage, ctr["image"])
	assert.Equal(t, false, ctr["securityContext"].(map[string]any)["allowPrivilegeEscalation"])
	assert.Equal(t, localGitName, localGitVolume().Name)

	// The seed script holds no secret: its token stays in a shell variable.
	assert.NotRegexp(t, `[0-9a-f]{40}`, localGitSeedScript)
	assert.Contains(t, localGitSeedScript, "GIT_ASKPASS=$ask")
	assert.Contains(t, localGitSeedScript, "--random-password")
}

// TestQuickstartLocal_DemoFiles: the seeded repository is laid out as
// pnz1990/kardinal-demo: environments/<env> with a kustomization over the
// kardinal-test-app Deployment at sha-9349a3f.
func TestQuickstartLocal_DemoFiles(t *testing.T) {
	t.Parallel()
	files, err := localDemoFiles()
	require.NoError(t, err)
	require.Len(t, files, 6)
	for _, env := range quickstartEnvs {
		k := files["environments/"+env+"/kustomization.yaml"]
		assert.Contains(t, k, "- deployment.yaml")
		assert.Contains(t, k, "newTag: sha-9349a3f")
		assert.Contains(t, files["environments/"+env+"/deployment.yaml"], "env: "+env)
	}
	objs, err := localGitObjects()
	require.NoError(t, err)
	cm := objs[0]
	require.Equal(t, "ConfigMap", cm.GetKind())
	data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
	assert.Len(t, data, 6)
	for k := range data {
		assert.Regexp(t, `^[A-Za-z0-9._-]+$`, k, "a ConfigMap key")
	}
}

func TestQuickstartLocal_Versions(t *testing.T) {
	t.Parallel()
	env, err := os.ReadFile("../../../hack/e2e/versions.env")
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^FORGEJO_IMAGE="?([^"\s]+)"?`).FindSubmatch(env)
	require.NotNil(t, m)
	image, digest, ok := strings.Cut(localGitImage, "@")
	require.True(t, ok, "the git server's image is pinned by digest")
	assert.Equal(t, string(m[1]), image, "hack/e2e/versions.env's FORGEJO_IMAGE")
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, digest)
	assert.Equal(t, 22, helmMinor("v3.22.0+g144ca65\n"))
	assert.Equal(t, 12, helmMinor("v3.12.3+g3a31588"))
	assert.Equal(t, 99, helmMinor("v4.0.1+g1234"))
	assert.Equal(t, -1, helmMinor("not helm"))
}

// TestQuickstartLocal_Installs: on an empty cluster quickstart --local
// installs kro, Argo CD, the git server and a kardinal-promoter for it, the
// controller's token from the server's user, and marks each part with its
// install ID: the Helm releases (--labels) and the namespaces it creates.
//
// Covers QUICKSTART-LOCAL-03.
func TestQuickstartLocal_Installs(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(defaultClass()).Build()
	tools := &fakeTools{t: t}
	setForTest(t, &quickstartRun, tools.run)
	q := &quickstart{ctx: context.Background(), w: &bytes.Buffer{}, c: c, kctx: "kind-kardinal", kubeconfig: "/tmp/qs-kubeconfig",
		o: quickstartOptions{local: true, chartVersion: "9.9.9"}}
	require.NoError(t, q.localPreflight())
	require.NoError(t, q.localBegin())
	id := q.id
	require.Regexp(t, `^[0-9a-f]{12}$`, id)
	require.NoError(t, q.kro())
	require.NoError(t, q.argoCD())
	require.NoError(t, q.gitServer())
	require.NoError(t, q.kardinal())

	all := strings.Join(tools.ran, "\n")
	assert.Contains(t, all, "helm upgrade --install kro oci://registry.k8s.io/kro/charts/kro --version 0.10.0-rc.0 --namespace kro-system "+
		"--create-namespace --wait --timeout 180s --labels kardinal.io/quickstart-id="+id)
	assert.Contains(t, all, "helm upgrade --install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --namespace kardinal-system --create-namespace "+
		"--set scm.provider=forgejo --set scm.apiURL=http://kardinal-git.kardinal-git.svc.cluster.local:3000 "+
		"--set scm.allowedRepositories={kardinal-git.kardinal-git.svc.cluster.local/kardinal/kardinal-demo} "+
		"--set scm.secretRef.name=kardinal-scm-token --wait --timeout 300s --labels kardinal.io/quickstart-id="+id+" --version 9.9.9")
	assert.NotContains(t, all, "kubectl create namespace", "--local creates its namespaces marked, through the API")
	for _, name := range []string{localGitNS, kroNamespace, "argocd", quickstartControllerNS} {
		var ns corev1.Namespace
		require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: name}, &ns))
		assert.Equal(t, id, ns.Labels[quickstartIDLabel], name)
	}
	var s corev1.Secret
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: quickstartControllerNS, Name: quickstartSCMSecret}, &s))
	assert.Equal(t, localTestTokens[0], s.StringData["token"])
	assert.Empty(t, s.Labels[v1alpha1.LabelSecretReferenceable], "no Pipeline may use the controller's token")
	assert.Equal(t, id, s.Labels[quickstartIDLabel])
	assert.Contains(t, all, "generate-access-token --username kardinal --token-name kardinal-controller-")
	assert.Contains(t, all, "--scopes write:repository,write:issue --raw")
	var pvc corev1.PersistentVolumeClaim
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: localGitNS, Name: localGitName}, &pvc))
}

// TestQuickstartLocal_Refuses: quickstart --local stops before it installs
// anything (no helm or kubectl install, no namespace) without a default
// StorageClass, with a kardinal-git namespace it did not create, with a
// kardinal-promoter for another git server, or with a helm too old to label
// a release.
//
// Covers QUICKSTART-LOCAL-03.
func TestQuickstartLocal_Refuses(t *testing.T) {
	cases := []struct {
		name   string
		objs   []sigs_client.Object
		values string
		helm   string
		want   string
		preNS  bool // the git server's namespace was there before
	}{
		{name: "no default StorageClass", want: "the cluster has no default StorageClass"},
		{name: "someone else's namespace", objs: []sigs_client.Object{defaultClass(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: localGitNS}}},
			want: "the namespace kardinal-git exists and quickstart did not create it", preNS: true},
		{name: "a kardinal-promoter for GitHub", objs: []sigs_client.Object{defaultClass()},
			values: `{"scm":{"provider":"github"}}`, want: "kardinal-promoter is already installed in kardinal-system (kardinal-promoter), and not for the in-cluster git server"},
		{name: "helm 3.12", objs: []sigs_client.Object{defaultClass()}, values: localHelmValuesJSON, helm: "v3.12.3+g3a31588",
			want: "quickstart --local needs helm 3.13 or later"},
		{name: "a helm version it cannot read", objs: []sigs_client.Object{defaultClass()}, values: localHelmValuesJSON, helm: "helm: command crashed",
			want: `helm version --short did not say which helm this is ("helm: command crashed")`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeKubeconfig(t, "kind-kardinal", "https://127.0.0.1:41234")
			noCluster(t)
			c := quickstartCluster(t, tc.objs...)
			setForTest(t, &quickstartClient, func(*clientcmdapi.Config) (sigs_client.Client, error) { return c, nil })
			tools := &fakeTools{t: t, helmValues: tc.values}
			run := tools.run
			if tc.helm != "" {
				run = func(ctx context.Context, name string, args ...string) (string, error) {
					if name == "helm" && len(args) > 0 && args[0] == "version" {
						return tc.helm, nil
					}
					return tools.run(ctx, name, args...)
				}
			}
			setForTest(t, &quickstartRun, run)
			err := quickstartFn(context.Background(), &bytes.Buffer{}, quickstartOptions{local: true, image: quickstartImage, noWait: true})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "nothing was changed")
			assert.Empty(t, tools.installs(), "nothing installed before the refusal")
			var ns corev1.Namespace
			err = c.Get(context.Background(), types.NamespacedName{Name: localGitNS}, &ns)
			assert.Equal(t, tc.preNS, err == nil)
			if err == nil {
				assert.Empty(t, ns.Labels[quickstartIDLabel], "not marked")
			}
		})
	}
}

// TestQuickstartLocal_Flags: --local and --repo are exclusive.
//
// Covers QUICKSTART-LOCAL-03.
func TestQuickstartLocal_Flags(t *testing.T) {
	t.Parallel()
	cmd := newQuickstartCmd()
	require.NoError(t, cmd.ParseFlags([]string{"--local", "--repo", "https://github.com/me/kardinal-demo"}))
	err := cmd.ValidateFlagGroups()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[local repo] were all set")
}

// markedRelease is the storage Secret of Helm release name in ns, revision
// 1, with install ID id ("" for a release quickstart did not install).
func markedRelease(name, ns, id string) *corev1.Secret {
	labels := map[string]string{"owner": "helm", "name": name, "version": "1", "status": "deployed"}
	if id != "" {
		labels[quickstartIDLabel] = id
	}
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "sh.helm.release.v1." + name + ".v1", Labels: labels}}
}

// argoObject is an Argo CD object of kind in argocd.
func argoObject(kind, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("argoproj.io/v1alpha1")
	u.SetKind(kind)
	u.SetNamespace("argocd")
	u.SetName(name)
	return u
}

// TestQuickstartLocal_Delete: quickstart --local --delete lists what it
// removes before it asks, and removes only what carries its install ID: the
// namespaces it created, the git server, and each Helm release (Argo CD: its
// namespace) it installed, CRDs included, only while no object of their API
// groups is anything but the demo's. A record or a namespace label edited by
// someone else removes nothing that is not marked.
//
// Covers QUICKSTART-LOCAL-03.
func TestQuickstartLocal_Delete(t *testing.T) {
	type world struct {
		releases   bool                 // kro's and kardinal's releases carry the ID
		ownedNS    bool                 // kro-system, argocd, kardinal-system carry it
		foreign    []sigs_client.Object // objects that are not the demo's
		forgeID    bool                 // the record's ID is edited after the install
		preexisted bool                 // kardinal-test-app-test was there before quickstart
	}
	all := []string{
		"helm uninstall kardinal-promoter --namespace kardinal-system --ignore-not-found --wait --timeout 300s",
		"kubectl delete -n argocd -f https://raw.githubusercontent.com/argoproj/argo-cd/v3.5.3/manifests/install.yaml --ignore-not-found --wait=false",
		"helm uninstall kro --namespace kro-system --ignore-not-found --wait --timeout 180s",
	}
	for _, tc := range []struct {
		name      string
		w         world
		uninstall []string
		keptNS    []string
		lines     []string
	}{
		{name: "it installed only the git server", w: world{},
			keptNS: []string{kroNamespace, "argocd", quickstartControllerNS},
			lines:  []string{"keep kardinal-promoter: this quickstart did not install it", "keep Argo CD v3.5.3: this quickstart did not install it"}},
		{name: "it installed everything", w: world{releases: true, ownedNS: true}, uninstall: all,
			lines: []string{"uninstall kardinal-promoter with its 2 CRDs and namespace kardinal-system",
				"uninstall Argo CD v3.5.3 with its 2 CRDs and namespace argocd", "uninstall kro 0.10.0-rc.0 with its 1 CRDs and namespace kro-system"}},
		{name: "an Application named as the demo's, not labelled with the install ID", w: world{releases: true, ownedNS: true,
			foreign: []sigs_client.Object{argoObject("Application", "kardinal-test-app-test")}},
			uninstall: []string{all[0], all[2]}, keptNS: []string{"argocd"},
			lines: []string{"keep Argo CD v3.5.3 and its CRDs: 1 object(s) that are not the demo's (Application argocd/kardinal-test-app-test)"}},
		{name: "other objects use Argo CD and kardinal", w: world{releases: true, ownedNS: true, foreign: []sigs_client.Object{
			argoObject("Application", "my-app"),
			&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "api"}},
		}}, uninstall: all[2:], keptNS: []string{"argocd", quickstartControllerNS},
			lines: []string{"keep kardinal-promoter and its CRDs: 1 object(s) that are not the demo's (Pipeline team-a/api)",
				"keep Argo CD v3.5.3 and its CRDs: 1 object(s) that are not the demo's (Application argocd/my-app)"}},
		{name: "a forged record", w: world{releases: true, ownedNS: true, forgeID: true},
			keptNS: append(quickstartWorkloadNamespaces(), kroNamespace, "argocd", quickstartControllerNS, localGitNS)},
		{name: "a namespace that was there before", w: world{releases: true, ownedNS: true, preexisted: true}, uninstall: all,
			keptNS: []string{"kardinal-test-app-test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pre []sigs_client.Object
			if tc.w.preexisted {
				pre = append(pre, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-test-app-test"}})
			}
			c, tools, _ := localDemo(t, pre...)
			ctx := context.Background()
			id := installID(t, c)
			// What the install would have marked, had it installed these.
			for _, name := range []string{kroNamespace, "argocd", quickstartControllerNS} {
				ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
				if tc.w.ownedNS {
					ns.Labels = map[string]string{quickstartMark: quickstartMarkValue, quickstartIDLabel: id}
				}
				require.NoError(t, c.Create(ctx, ns))
			}
			relID := ""
			if tc.w.releases {
				relID = id
			}
			require.NoError(t, c.Create(ctx, markedRelease("kardinal-promoter", quickstartControllerNS, relID)))
			require.NoError(t, c.Create(ctx, markedRelease("kro", kroNamespace, relID)))
			// The demo's own Argo CD objects: the default AppProject.
			require.NoError(t, c.Create(ctx, argoObject("AppProject", "default")))
			for _, o := range tc.w.foreign {
				require.NoError(t, c.Create(ctx, o))
			}
			if tc.w.forgeID {
				var cm corev1.ConfigMap
				require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: localGitNS, Name: quickstartRecord}, &cm))
				cm.Data["id"] = "000000000000"
				require.NoError(t, c.Update(ctx, &cm))
			}
			// A record in default, as the first version of --local wrote it,
			// does nothing.
			require.NoError(t, c.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: quickstartRecord,
				Labels: map[string]string{quickstartMark: quickstartMarkValue}}, Data: map[string]string{"installed": "kro argocd kardinal-promoter"}}))
			tools.ran = nil

			var out bytes.Buffer
			asked := ""
			require.NoError(t, quickstartDeleteFn(ctx, &out, func(q string) error {
				asked = out.String()
				return nil
			}, true))
			got := withoutKubeconfig(out.String())
			// The list comes before the question.
			assert.Contains(t, asked, "quickstart --local --delete will, in kind-kardinal:\n  delete the demo")
			for _, l := range tc.lines {
				assert.Contains(t, asked, "  "+l+"\n")
			}
			ran := strings.Join(tools.ran, "\n")
			for _, l := range all {
				assert.Equal(t, contains(tc.uninstall, l), strings.Contains(ran, l), l)
			}
			var ns corev1.Namespace
			for _, name := range append(append(quickstartWorkloadNamespaces(), localGitNS), kroNamespace, "argocd", quickstartControllerNS) {
				kept := contains(tc.keptNS, name)
				assert.Equal(t, !kept, gone(t, c, &ns, types.NamespacedName{Name: name}), "namespace %s deleted: %v", name, !kept)
			}
			var s corev1.Secret
			assert.True(t, gone(t, c, &s, types.NamespacedName{Namespace: "default", Name: localGitSecret}), "the demo goes in every case")
			if tc.w.forgeID {
				assert.Contains(t, got, "No quickstart --local install here")
			} else {
				assert.Contains(t, got, "To set the demo up again: kardinal --context kind-kardinal quickstart --local")
			}
		})
	}
}

// contains reports whether s holds v.
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestQuickstartLocal_DeleteNeedsYesOffATerminal: --local --delete
// uninstalls cluster components, so without a terminal to ask on it stops,
// with nothing deleted, unless --yes is given; plain --delete keeps its
// behaviour (no question off a terminal).
//
// Covers QUICKSTART-LOCAL-03.
func TestQuickstartLocal_DeleteNeedsYesOffATerminal(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		local, tty bool
		refused    bool
	}{
		{name: "--local --delete off a terminal", args: []string{"--local", "--delete"}, local: true, refused: true},
		{name: "--local --delete --yes off a terminal", args: []string{"--local", "--delete", "--yes"}, local: true},
		{name: "--delete off a terminal", args: []string{"--delete"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := localDemo(t)
			setForTest(t, &promptable, func() bool { return tc.tty })
			cmd := newQuickstartCmd()
			require.NoError(t, cmd.ParseFlags(tc.args))
			var out bytes.Buffer
			err := quickstartDeleteFn(context.Background(), &out, deleteAsk(cmd, tc.local), tc.local)
			var p v1alpha1.Pipeline
			if tc.refused {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "pass --yes; nothing was deleted")
				assert.Contains(t, out.String(), "will, in kind-kardinal:\n", "the list comes first")
				require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "demo"}, &p), "nothing deleted")
				var ns corev1.Namespace
				require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: localGitNS}, &ns))
				return
			}
			require.NoError(t, err)
			assert.True(t, gone(t, c, &p, types.NamespacedName{Namespace: "default", Name: "demo"}))
		})
	}
}
