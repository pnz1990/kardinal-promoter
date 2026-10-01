// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakediscovery "k8s.io/client-go/discovery/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func doctorClient(objs ...sigs_client.Object) sigs_client.Client {
	return fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(objs...).Build()
}

func kroPod(name string, phase corev1.PodPhase, images ...string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kro-system"}}
	for i, img := range images {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "c" + string(rune('0'+i)), Image: img})
	}
	p.Status.Phase = phase
	return p
}

func versionConfigMap(ns, version string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "kardinal-version", Namespace: ns},
		Data:       map[string]string{"version": version},
	}
}

// E2E-07: version reports the kro version doctor finds, and reads the
// controller ConfigMap from the controller namespace.
func TestClusterVersions_ReportsKroAndControllerNamespace(t *testing.T) {
	c := doctorClient(
		versionConfigMap("kardinal", "v0.6.0"),
		kroPod("kro-7d9f-abcde", corev1.PodRunning, "registry.k8s.io/kro/kro:v0.10.0-rc.0"),
	)
	controllerVer, graphVer := clusterVersions(context.Background(), c, "kardinal")
	assert.Equal(t, "v0.6.0", controllerVer)
	assert.Equal(t, "kro v0.10.0-rc.0", graphVer)

	var buf bytes.Buffer
	require.NoError(t, versionFn(&buf, controllerVer, graphVer))
	assert.Contains(t, buf.String(), "Graph:      kro v0.10.0-rc.0\n")

	controllerVer, _ = clusterVersions(context.Background(), c, defaultControllerNamespace)
	assert.Equal(t, "unknown", controllerVer, "no ConfigMap in kardinal-system")
}

// C09a-cli-11: the kro version is the tag of the kro image, not of whatever
// container comes last in any pod whose name contains "kro".
func TestKroVersion(t *testing.T) {
	cases := []struct {
		name      string
		pods      []sigs_client.Object
		wantVer   string
		wantFound bool
	}{
		{
			name:    "kro image with a sidecar after it",
			pods:    []sigs_client.Object{kroPod("kro-1", corev1.PodRunning, "registry.k8s.io/kro/kro:v0.10.0", "envoy:v1.30")},
			wantVer: "v0.10.0", wantFound: true,
		},
		{
			name:    "registry port, digest",
			pods:    []sigs_client.Object{kroPod("kro-1", corev1.PodRunning, "localhost:5000/kro:v0.9.1@sha256:abc")},
			wantVer: "v0.9.1", wantFound: true,
		},
		{
			name:    "digest only has no tag",
			pods:    []sigs_client.Object{kroPod("kro-1", corev1.PodRunning, "localhost:5000/kro@sha256:abc")},
			wantVer: "", wantFound: true,
		},
		{
			name: "pending kro pod and an unrelated running pod",
			pods: []sigs_client.Object{
				kroPod("kro-1", corev1.PodPending, "registry.k8s.io/kro/kro:v0.10.0"),
				kroPod("kro-metrics-proxy", corev1.PodRunning, "quay.io/brancz/kube-rbac-proxy:v0.18.0"),
			},
		},
		{name: "no pods"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ver, found, err := kroVersion(context.Background(), doctorClient(tc.pods...))
			require.NoError(t, err)
			assert.Equal(t, tc.wantVer, ver)
			assert.Equal(t, tc.wantFound, found)
		})
	}
}

// C09a-cli-10: the controller check reads the release namespace.
func TestCheckController_ControllerNamespace(t *testing.T) {
	c := doctorClient(versionConfigMap("kardinal", "v0.6.0"))

	r := checkController(context.Background(), c, "kardinal")
	assert.Equal(t, doctorPass, r.icon)
	assert.Equal(t, "kardinal-promoter v0.6.0 in kardinal", r.detail)

	r = checkController(context.Background(), c, defaultControllerNamespace)
	assert.True(t, r.failed)
	assert.Contains(t, r.hint, "--controller-namespace")
}

// The install hint pins the chart to a release CLI's version, so a v0.9.0-rc.1
// CLI does not suggest the newest final chart.
func TestInstallCommand(t *testing.T) {
	tests := []struct {
		name, version, want string
	}{
		{"release candidate", "v0.9.0-rc.1",
			"helm upgrade --install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0-rc.1 --namespace kardinal-system --create-namespace"},
		{"final", "v0.9.0",
			"helm upgrade --install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 --namespace kardinal-system --create-namespace"},
		{"dev build", "v0.1.0-dev",
			"helm upgrade --install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --namespace kardinal-system --create-namespace"},
		{"empty", "",
			"helm upgrade --install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --namespace kardinal-system --create-namespace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, installCommand(tc.version, "kardinal-system"))
		})
	}
}

func controllerDeployment(ns string, env ...corev1.EnvVar) *appsv1.Deployment {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "kardinal-promoter", Namespace: ns,
		Labels: map[string]string{"app.kubernetes.io/name": "kardinal-promoter"},
	}}
	d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "controller", Image: "x", Env: env}}
	return d
}

func secretRefEnv(name, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: "GITHUB_TOKEN", ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key},
	}}
}

// C09a-cli-10: the token check follows the controller Deployment's
// GITHUB_TOKEN (inline value or any Secret name) in the release namespace.
func TestCheckGitHubToken(t *testing.T) {
	secret := func(name, key, value string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kardinal"},
			Data:       map[string][]byte{key: []byte(value)},
		}
	}
	cases := []struct {
		name       string
		objs       []sigs_client.Object
		wantIcon   string
		wantDetail string
	}{
		{
			name:     "inline value (helm github.token)",
			objs:     []sigs_client.Object{controllerDeployment("kardinal", corev1.EnvVar{Name: "GITHUB_TOKEN", Value: "ghp_x"})},
			wantIcon: doctorPass, wantDetail: "GITHUB_TOKEN set inline on Deployment kardinal-promoter",
		},
		{
			name: "custom secretRef",
			objs: []sigs_client.Object{
				controllerDeployment("kardinal", secretRefEnv("scm-creds", "pat")),
				secret("scm-creds", "pat", "ghp_x"),
			},
			wantIcon: doctorPass, wantDetail: "secret scm-creds (key pat) present in kardinal",
		},
		{
			name:     "secret missing",
			objs:     []sigs_client.Object{controllerDeployment("kardinal", secretRefEnv("scm-creds", "pat"))},
			wantIcon: doctorWarn, wantDetail: "secret scm-creds not found in kardinal",
		},
		{
			name: "secret key empty",
			objs: []sigs_client.Object{
				controllerDeployment("kardinal", secretRefEnv("scm-creds", "pat")),
				secret("scm-creds", "token", "ghp_x"),
			},
			wantIcon: doctorWarn, wantDetail: `secret scm-creds has no "pat" key or it is empty`,
		},
		{
			name:     "no GITHUB_TOKEN",
			objs:     []sigs_client.Object{controllerDeployment("kardinal")},
			wantIcon: doctorWarn, wantDetail: "GITHUB_TOKEN is not set on Deployment kardinal-promoter",
		},
		{
			name:     "no controller in the namespace",
			objs:     []sigs_client.Object{controllerDeployment("kardinal-system")},
			wantIcon: doctorWarn, wantDetail: "no kardinal-promoter Deployment in kardinal",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := checkGitHubToken(context.Background(), doctorClient(tc.objs...), "kardinal")
			assert.Equal(t, tc.wantIcon, r.icon)
			assert.Equal(t, tc.wantDetail, r.detail)
			assert.NotContains(t, r.detail+r.hint, "ghp_x", "the token is never printed")
			wantLabel := "GitHub token"
			if tc.name == "no controller in the namespace" {
				wantLabel = "SCM token"
			}
			assert.Equal(t, wantLabel, r.label, "no --scm-provider is GitHub")
		})
	}
}

// The token check is named after the controller's --scm-provider, read the
// way the controller reads it: the last flag in command and args, either
// form, else KARDINAL_SCM_PROVIDER. A provider doctor does not know, or no
// controller Deployment, is "SCM token".
func TestCheckGitHubToken_ProviderLabel(t *testing.T) {
	cases := []struct {
		name      string
		command   []string
		args      []string
		env       []corev1.EnvVar
		wantLabel string
	}{
		{name: "default", wantLabel: "GitHub token"},
		{name: "github", args: []string{"--scm-provider=github"}, wantLabel: "GitHub token"},
		{name: "chart args", args: []string{"--leader-elect=true", "--scm-provider=forgejo", "--scm-api-url=http://x"},
			wantLabel: "Forgejo token"},
		{name: "gitea", args: []string{"--scm-provider=gitea"}, wantLabel: "Gitea token"},
		{name: "separate value", args: []string{"-scm-provider", "gitlab"}, wantLabel: "GitLab token"},
		{name: "in command", command: []string{"/manager", "--scm-provider=bitbucket"}, wantLabel: "Bitbucket token"},
		{name: "last flag wins", args: []string{"--scm-provider=gitlab", "--scm-provider=azuredevops"},
			wantLabel: "Azure DevOps token"},
		{name: "env", env: []corev1.EnvVar{{Name: "KARDINAL_SCM_PROVIDER", Value: "gitlab"}}, wantLabel: "GitLab token"},
		{name: "flag beats env", args: []string{"--scm-provider=forgejo"},
			env: []corev1.EnvVar{{Name: "KARDINAL_SCM_PROVIDER", Value: "gitlab"}}, wantLabel: "Forgejo token"},
		{name: "unknown provider", args: []string{"--scm-provider=svn"}, wantLabel: "SCM token"},
		{name: "a flag with a similar name", args: []string{"--scm-provider-x=gitlab"}, wantLabel: "GitHub token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := controllerDeployment("kardinal", append(tc.env, corev1.EnvVar{Name: "GITHUB_TOKEN", Value: "ghp_x"})...)
			d.Spec.Template.Spec.Containers[0].Command = tc.command
			d.Spec.Template.Spec.Containers[0].Args = tc.args
			r := checkGitHubToken(context.Background(), doctorClient(d), "kardinal")
			assert.Equal(t, tc.wantLabel, r.label)
			assert.Equal(t, doctorPass, r.icon)
		})
	}
	r := checkGitHubToken(context.Background(), doctorClient(), "kardinal")
	assert.Equal(t, "SCM token", r.label, "no controller Deployment")
}

// C09a-cli-11: pipeline phases are Ready, Degraded, Promoting, Unknown; a Get error other
// than NotFound is reported as such. The Ready condition comes first: a
// Pipeline the controller refused fails whatever its phase, and one it has not
// reconciled at its current generation warns.
func TestCheckPipelineHealth(t *testing.T) {
	withReady := func(phase string, status metav1.ConditionStatus, reason, msg string, observed int64) *v1alpha1.Pipeline {
		p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team-a", Generation: 2}}
		p.Status.Phase = phase
		p.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: status, Reason: reason, Message: msg,
			ObservedGeneration: observed}}
		return p
	}
	pipe := func(phase string) *v1alpha1.Pipeline {
		return withReady(phase, metav1.ConditionTrue, "Valid", "Pipeline spec is valid", 2)
	}
	noCondition := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team-a"}}
	cases := []struct {
		name       string
		client     sigs_client.Client
		wantIcon   string
		wantDetail string
	}{
		{name: "ready", client: doctorClient(pipe("Ready")), wantIcon: doctorPass, wantDetail: "status: Ready"},
		{name: "degraded", client: doctorClient(pipe("Degraded")), wantIcon: doctorWarn, wantDetail: "status: Degraded"},
		// E2E-R05: a Bundle in flight or held by a gate.
		{name: "promoting", client: doctorClient(pipe("Promoting")), wantIcon: doctorPass, wantDetail: "status: Promoting"},
		// Unknown is the phase before the first Bundle.
		{name: "unknown", client: doctorClient(pipe("Unknown")), wantIcon: doctorPass,
			wantDetail: "status: Unknown (spec valid, no Bundle yet)"},
		{name: "unset", client: doctorClient(pipe("")), wantIcon: doctorPass,
			wantDetail: "status: Unknown (spec valid, no Bundle yet)"},
		{name: "no Ready condition", client: doctorClient(noCondition), wantIcon: doctorWarn,
			wantDetail: "not yet reconciled by the controller"},
		{name: "condition of an older generation",
			client:   doctorClient(withReady("Ready", metav1.ConditionTrue, "Valid", "Pipeline spec is valid", 1)),
			wantIcon: doctorWarn, wantDetail: "not yet reconciled by the controller"},
		{name: "refused", client: doctorClient(withReady("Unknown", metav1.ConditionFalse, "ValidationFailed",
			`git.secretRef.namespace "x" is not allowed`, 2)),
			wantIcon: doctorFail, wantDetail: `Ready=False (ValidationFailed): git.secretRef.namespace "x" is not allowed`},
		{name: "refused after a promotion", client: doctorClient(withReady("Ready", metav1.ConditionFalse, "NotImplemented",
			"not implemented", 2)),
			wantIcon: doctorFail, wantDetail: "Ready=False (NotImplemented): not implemented"},
		{name: "not found", client: doctorClient(), wantIcon: doctorFail,
			wantDetail: `Pipeline "web" not found in namespace "team-a"`},
		{
			name: "forbidden",
			client: fake.NewClientBuilder().WithScheme(rootScheme).WithInterceptorFuncs(interceptor.Funcs{
				Get: func(context.Context, sigs_client.WithWatch, sigs_client.ObjectKey, sigs_client.Object, ...sigs_client.GetOption) error {
					return errors.New("pipelines.kardinal.io is forbidden")
				},
			}).Build(),
			wantIcon:   doctorFail,
			wantDetail: `get Pipeline "web" in namespace "team-a" failed: pipelines.kardinal.io is forbidden`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := checkPipelineHealth(context.Background(), tc.client, "team-a", "web")
			assert.Equal(t, tc.wantIcon, r.icon)
			assert.Equal(t, tc.wantDetail, r.detail)
		})
	}
}

// C09a-cli-11: the CRD check verifies each resource, not just the group.
func TestCheckKardinalCRDs(t *testing.T) {
	disco := func(names ...string) *fakediscovery.FakeDiscovery {
		list := &metav1.APIResourceList{GroupVersion: v1alpha1.GroupVersion.String()}
		for _, n := range names {
			list.APIResources = append(list.APIResources, metav1.APIResource{Name: n})
		}
		return &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{Resources: []*metav1.APIResourceList{list}}}
	}

	r := checkKardinalCRDs(disco(kardinalResources...))
	assert.Equal(t, doctorPass, r.icon)

	r = checkKardinalCRDs(disco("pipelines", "bundles"))
	assert.True(t, r.failed)
	assert.Contains(t, r.detail, "missing: auditevents, changewindows")
	assert.Contains(t, r.detail, "promotionsteps")
}

// kardinalResources lists every CRD in config/crd/bases.
func TestKardinalResources_MatchCRDs(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "config", "crd", "bases", "kardinal.io_*.yaml"))
	require.NoError(t, err)
	var want []string
	for _, f := range files {
		want = append(want, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "kardinal.io_"), ".yaml"))
	}
	sort.Strings(want)
	got := append([]string(nil), kardinalResources...)
	sort.Strings(got)
	assert.Equal(t, want, got)
}

// kroMinVersion is the version hack/install-kro.sh installs.
func TestKroMinVersion_MatchesInstallScript(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "hack", "install-kro.sh"))
	require.NoError(t, err)
	m := regexp.MustCompile(`KRO_VERSION:-([0-9][0-9a-z.-]*)`).FindSubmatch(script)
	require.NotNil(t, m)
	assert.Equal(t, string(m[1]), kroMinVersion)
	assert.Contains(t, kroInstallHint, "v"+kroMinVersion)
}

// C09a-cli-09 / C09b-cli-21: the namespace comes from the kubeconfig context,
// and a broken kubeconfig is reported instead of the in-cluster error.
func TestResolveRestConfig(t *testing.T) {
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["c"] = &clientcmdapi.Cluster{Server: "https://127.0.0.1:1"}
	cfg.AuthInfos["u"] = &clientcmdapi.AuthInfo{}
	cfg.Contexts["team-a"] = &clientcmdapi.Context{Cluster: "c", AuthInfo: "u", Namespace: "team-a"}
	cfg.CurrentContext = "team-a"

	_, ns, err := resolveRestConfig(clientcmd.NewDefaultClientConfig(*cfg, &clientcmd.ConfigOverrides{}), false, "")
	require.NoError(t, err)
	assert.Equal(t, "team-a", ns)

	_, ns, err = resolveRestConfig(clientcmd.NewDefaultClientConfig(*cfg, &clientcmd.ConfigOverrides{}), false, "flag-ns")
	require.NoError(t, err)
	assert.Equal(t, "flag-ns", ns)

	_, _, err = resolveRestConfig(clientcmd.NewDefaultClientConfig(*cfg,
		&clientcmd.ConfigOverrides{CurrentContext: "missing"}), false, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `context "missing" does not exist`)
	assert.NotContains(t, err.Error(), "KUBERNETES_SERVICE_HOST")
}

func TestBuildRestConfig_ExplicitMissingKubeconfig(t *testing.T) {
	old := globalKubeconfig
	t.Cleanup(func() { globalKubeconfig = old })
	globalKubeconfig = filepath.Join(t.TempDir(), "nonexistent")

	_, _, err := buildRestConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
	assert.NotContains(t, err.Error(), "KUBERNETES_SERVICE_HOST")
}

// C09a-cli-23: doctorWarn has no trailing space, so columns line up.
func TestDoctorIcons_NoTrailingSpace(t *testing.T) {
	for _, icon := range []string{doctorPass, doctorWarn, doctorFail} {
		assert.Equal(t, strings.TrimSpace(icon), icon)
	}
}
