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
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
)

// What kardinal quickstart installs. quickstartKroVersion is hack/install-kro.sh's
// KRO_VERSION and argoCDRelease hack/e2e/versions.env's ARGOCD_RELEASE
// (TestQuickstart_Versions checks both).
const (
	quickstartKroVersion = "0.10.0-rc.0"
	kroChart             = "oci://registry.k8s.io/kro/charts/kro"
	argoCDRelease        = "v3.5.3"
	// quickstartChart is the published kardinal-promoter chart.
	quickstartChart = "oci://ghcr.io/pnz1990/charts/kardinal-promoter"
	// quickstartControllerNS is the release namespace of the chart.
	quickstartControllerNS = "kardinal-system"
	// The demo lives in default, so no command needs -n.
	quickstartNS       = "default"
	quickstartPipeline = "demo"
	quickstartSecret   = "github-token"
	quickstartGate     = "prod-approval"
	// quickstartAppSet deploys kardinal-demo's environments as Argo CD
	// Applications kardinal-test-app-<env>, which the Pipeline checks.
	quickstartAppSet = "kardinal-test-app"
)

// quickstartImages are the published kardinal-test-app images quickstart
// knows, in order: it ships the first and prints the command that ships the
// second. A new test-app release is one more line here. Today there is one
// build (the one kardinal-demo's overlays run, sha-9349a3f), so both are it,
// by digest: pinning the digest changes the overlays, so the first one
// promotes, and the second is the same bits under another real tag.
var quickstartImages = []string{
	"ghcr.io/pnz1990/kardinal-test-app:sha-9349a3f@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0",
	"ghcr.io/pnz1990/kardinal-test-app:main@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0",
}

// quickstartImage is the image quickstart ships first.
var quickstartImage = quickstartImages[0]

// nextImage is the image after image in quickstartImages (the last one is
// followed by itself).
func nextImage(image string) string {
	for i, img := range quickstartImages {
		if img == image && i+1 < len(quickstartImages) {
			return quickstartImages[i+1]
		}
	}
	return quickstartImages[len(quickstartImages)-1]
}

// kroNamespace is where hack/install-kro.sh installs kro.
const kroNamespace = "kro-system"

// kroHelmValues are the kro chart values hack/install-kro.sh sets, at its
// defaults (TestQuickstart_Versions checks them).
var kroHelmValues = []string{
	"config.featureGates.GraphKind=true",
	"rbac.mode=aggregation",
	"config.graphConcurrentReconciles=8",
	"config.clientQps=300",
	"config.clientBurst=500",
	"deployment.resources.limits.memory=2Gi",
	"deployment.resources.requests.memory=768Mi",
}

// quickstartSCMSecret is the controller's own SCM token Secret, in
// quickstartControllerNS (scm.secretRef.name): the token it opens and merges
// PRs with, apart from the Pipeline's git Secret.
const quickstartSCMSecret = "kardinal-scm-token"

// quickstartEnvs are kardinal-demo's environments, in promotion order.
var quickstartEnvs = []string{"test", "uat", "prod"}

// quickstartRun runs helm or kubectl for quickstart, with scrubbedEnv, and
// returns its output; a test replaces it.
var quickstartRun = func(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = toolEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// quickstartLookPath finds helm and kubectl; a test replaces it.
var quickstartLookPath = exec.LookPath

// quickstartOptions are the flags of kardinal quickstart.
type quickstartOptions struct {
	repo, image, chart, chartVersion string
	sets                             []string
	noArgoCD, noWait                 bool
	timeout                          time.Duration
}

func newQuickstartCmd() *cobra.Command {
	o := quickstartOptions{timeout: 15 * time.Minute}
	cmd := &cobra.Command{
		Use:   "quickstart --repo <your fork of kardinal-demo>",
		Short: "Install kardinal on a local cluster and promote a first version",
		Long: `Set up a demo on a local cluster (kind, k3d, minikube, Docker or Rancher
Desktop; any other context is refused before anything is written): kro, Argo
CD, kardinal, the git Secret, the Argo CD Applications of your kardinal-demo
fork, a Pipeline with an approval gate on prod, and a first version, which it
follows until prod waits for your approval. A re-run changes nothing.`,
		Example: `  kind create cluster --name kardinal
  kardinal quickstart --repo https://github.com/<you>/kardinal-demo
  kardinal approve demo prod`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Ctrl-C or SIGTERM cancels the step that runs (helm, kubectl or
			// the follow) and quickstart returns, removing its kubeconfig file.
			ctx, stop := signal.NotifyContext(contextOf(cmd), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return quickstartFn(ctx, cmd.OutOrStdout(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.repo, "repo", "", "Your fork of github.com/pnz1990/kardinal-demo (the token needs contents and pull requests read and write on it)")
	f.StringVar(&o.image, "image", quickstartImage, "The first version to ship")
	f.StringVar(&o.chart, "chart", "", "The kardinal-promoter Helm chart, an OCI reference or a directory (default: the published chart, ghcr.io/pnz1990/charts)")
	f.StringVar(&o.chartVersion, "chart-version", "", "Chart version (default: this CLI's version; the newest for a development build)")
	f.StringArrayVar(&o.sets, "set", nil, "A Helm value for the kardinal-promoter chart (key=value; repeatable)")
	f.BoolVar(&o.noArgoCD, "no-argocd", false, "Do not install Argo CD (the cluster has one)")
	f.BoolVar(&o.noWait, "no-wait", false, "Do not follow the first version")
	f.Var(durationValue{&o.timeout}, "timeout", "How long to follow the first version")
	withReference(cmd, quickstartReference)
	return cmd
}

// localContextName reports whether name is a context name quickstart accepts.
func localContextName(name string) bool {
	switch {
	case strings.HasPrefix(name, "kind-"), strings.HasPrefix(name, "k3d-"):
		return true
	}
	switch name {
	case "minikube", "docker-desktop", "rancher-desktop":
		return true
	}
	return false
}

// localServer reports whether server, the API server URL of context name,
// is on this machine: loopback, 0.0.0.0 (k3d's default), localhost or
// Docker Desktop's names; for minikube also an address in its drivers'
// networks (minikubeNetworks). A tunnel from a loopback port to a remote
// cluster looks local: that cannot be told from the kubeconfig.
func localServer(name, server string) bool {
	u, err := url.Parse(server)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	switch host {
	case "localhost", "host.docker.internal", "kubernetes.docker.internal":
		return true
	}
	ip := net.ParseIP(host)
	switch {
	case ip == nil:
		return false
	case ip.IsLoopback(), ip.IsUnspecified():
		return true
	}
	if name != "minikube" {
		return false
	}
	for _, r := range minikubeNetworks {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

// minikubeNetworks are the networks minikube's drivers put its API server
// on: docker and podman (192.168.49.0/24), kvm2 (192.168.39.0/24) and
// hyperkit, hyperv, virtualbox, qemu (192.168.59.0/24). Any other private
// address may be a remote cluster.
var minikubeNetworks = func() []*net.IPNet {
	var out []*net.IPNet
	for _, cidr := range []string{"192.168.49.0/24", "192.168.39.0/24", "192.168.59.0/24"} {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}()

// currentContext is the kubeconfig context the CLI uses (--context, else
// the current one), its API server URL, and the kubeconfig with only that
// context, read from the kubeconfig only.
func currentContext() (name, server string, only *clientcmdapi.Config, err error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if globalKubeconfig != "" {
		rules.ExplicitPath = globalKubeconfig
	}
	raw, err := rules.Load()
	if err != nil {
		return "", "", nil, fmt.Errorf("read the kubeconfig: %w", err)
	}
	name = globalContext
	if name == "" {
		name = raw.CurrentContext
	}
	kc, ok := raw.Contexts[name]
	if name == "" || !ok {
		return name, "", nil, fmt.Errorf("no kubeconfig context to install into; create a local cluster: kind create cluster --name kardinal")
	}
	if cl, ok := raw.Clusters[kc.Cluster]; ok {
		server = cl.Server
	}
	only = raw.DeepCopy()
	only.CurrentContext = name
	if err := clientcmdapi.MinifyConfig(only); err != nil {
		return "", "", nil, fmt.Errorf("read the kubeconfig: %w", err)
	}
	if err := clientcmdapi.FlattenConfig(only); err != nil {
		return "", "", nil, fmt.Errorf("read the kubeconfig: %w", err)
	}
	return name, server, only, nil
}

// targetOverrides are the environment variables that point helm, kubectl or
// client-go at another cluster than the kubeconfig context: helm's
// HELM_KUBE* (HELM_KUBECONTEXT, HELM_KUBEAPISERVER, HELM_KUBETOKEN, ...),
// HELM_KUBECONFIG-like names, and KUBERNETES_MASTER. quickstart refuses to
// run with one set (it checks the context, not them) and clears them for
// helm and kubectl.
func targetOverrides(environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		if v == "" {
			continue
		}
		if strings.HasPrefix(k, "HELM_KUBE") || k == "KUBERNETES_MASTER" {
			out = append(out, k)
		}
	}
	return out
}

// toolEnv is the environment helm and kubectl run with: scrubbedEnv, and
// KUBERC=off, so no kuberc alias or default changes what kubectl runs.
func toolEnv(environ []string) []string {
	return append(scrubbedEnv(environ), "KUBERC=off")
}

// scrubbedEnv is environ without what could point helm or kubectl at
// another cluster or change their commands: the targetOverrides, KUBECONFIG
// (quickstart passes --kubeconfig) and KUBERC (quickstartRun sets it off).
func scrubbedEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "HELM_KUBE") || k == "KUBERNETES_MASTER" || k == "KUBECONFIG" || k == "KUBERC" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// errNotLocal is quickstart's refusal of a context that is not a local
// cluster.
var errNotLocal = errors.New("not a local cluster")

// requireLocalContext refuses a context that is not a local cluster: its
// name is not kind-*, k3d-*, minikube, docker-desktop or rancher-desktop, or
// its server is not on this machine (localServer), and refuses to run with
// a variable set that could point helm or kubectl elsewhere
// (targetOverrides). It reads only the kubeconfig and the environment:
// nothing is sent to any cluster before it passes. It returns the context and
// the kubeconfig with only it, which every later step uses.
func requireLocalContext() (string, *clientcmdapi.Config, error) {
	if set := targetOverrides(os.Environ()); len(set) > 0 {
		return "", nil, fmt.Errorf("kardinal quickstart does not run with %s set: helm or kubectl would use it instead of the kubeconfig context it checks, "+
			"and nothing was changed. Unset it: unset %s: %w", strings.Join(set, ", "), strings.Join(set, " "), errNotLocal)
	}
	name, server, only, err := currentContext()
	if err != nil {
		return "", nil, err
	}
	if !localContextName(name) || !localServer(name, server) {
		return "", nil, fmt.Errorf("kardinal quickstart installs a demo into a local cluster only (kind, k3d, minikube, Docker Desktop, Rancher Desktop); "+
			"the context %q (server %s) is not one, and nothing was changed. Create a local cluster and use it: "+
			"kind create cluster --name kardinal: %w", name, server, errNotLocal)
	}
	// A proxy (proxy-url) could send the requests to any cluster, wherever
	// the server URL points.
	if kc := only.Contexts[name]; kc != nil {
		if cl := only.Clusters[kc.Cluster]; cl != nil && cl.ProxyURL != "" {
			return "", nil, fmt.Errorf("kardinal quickstart does not use a kubeconfig context with a proxy: the context %q sends its requests through %s, "+
				"so it may not be the local cluster it names, and nothing was changed. Use a local context without proxy-url: %w", name, cl.ProxyURL, errNotLocal)
		}
	}
	return name, only, nil
}

// quickstartClient is the cluster client of quickstart, from the kubeconfig
// with only the checked context; a test replaces it.
var quickstartClient = func(only *clientcmdapi.Config) (sigs_client.Client, error) {
	cfg, err := clientcmd.NewDefaultClientConfig(*only, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load the kubeconfig: %w", err)
	}
	c, err := sigs_client.New(cfg, sigs_client.Options{Scheme: rootScheme})
	if err != nil {
		return nil, fmt.Errorf("create the Kubernetes client: %w", err)
	}
	return c, nil
}

// quickstartFn is kardinal quickstart.
func quickstartFn(ctx context.Context, w io.Writer, o quickstartOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// 1. Before anything else: a local cluster only.
	kctx, only, err := requireLocalContext()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(o.repo, "https://") && !strings.HasPrefix(o.repo, "http://") {
		return fmt.Errorf("quickstart needs --repo, your fork of github.com/pnz1990/kardinal-demo: kardinal quickstart --repo https://github.com/<you>/kardinal-demo")
	}
	for _, tool := range []string{"helm", "kubectl"} {
		if _, err := quickstartLookPath(tool); err != nil {
			return fmt.Errorf("quickstart needs %s on PATH to install kro, Argo CD and kardinal: see https://helm.sh/docs/intro/install/ and https://kubernetes.io/docs/tasks/tools/", tool)
		}
	}
	if _, _, err := gitToken(); err != nil {
		return fmt.Errorf("quickstart needs a GitHub token with contents and pull requests read and write on %s: "+
			"export GITHUB_TOKEN=<token>, or log in with gh auth login", o.repo)
	}
	c, err := quickstartClient(only)
	if err != nil {
		return fmt.Errorf("quickstart: %w", err)
	}
	// helm and kubectl get the checked context alone, in a file of their
	// own (0600, removed at the end), and an environment without overrides.
	kubeconfig, err := writeHelmKubeconfig(only)
	if err != nil {
		return fmt.Errorf("quickstart: %w", err)
	}
	defer func() { _ = os.Remove(kubeconfig) }()
	q := &quickstart{ctx: ctx, w: w, c: c, kctx: kctx, kubeconfig: kubeconfig, o: o}
	_, _ = fmt.Fprintf(w, "Setting up the kardinal demo in %s\n", kctx)

	// 2. kro, Argo CD, kardinal: each only when it is not there.
	for _, step := range []func() error{q.kro, q.argoCD, q.kardinal} {
		if err := step(); err != nil {
			return err
		}
	}
	// 3. The demo: Secret, Applications, gate, Pipeline.
	if err := q.secret(); err != nil {
		return err
	}
	for _, step := range []func() error{q.applicationSet, q.gate, q.pipeline} {
		if err := step(); err != nil {
			return err
		}
	}
	// 4. The first version, followed until prod waits for the approval.
	if err := q.ship(); err != nil {
		return err
	}
	q.next()
	return nil
}

// quickstart is one run of kardinal quickstart.
type quickstart struct {
	ctx  context.Context
	w    io.Writer
	c    sigs_client.Client
	kctx string
	// kubeconfig is the file with only kctx that helm and kubectl use.
	kubeconfig string
	o          quickstartOptions
}

// say writes one line of quickstart's progress: what, and what happened.
func (q *quickstart) say(what, done string) {
	_, _ = fmt.Fprintf(q.w, "  %s %s: %s\n", sty.Paint(toneOf(done), sty.Sym("✓", "-")), what, done)
}

// toneOf is the colour of a progress line: quiet when nothing changed.
func toneOf(done string) display.Tone {
	if strings.HasPrefix(done, "already") || strings.HasPrefix(done, "kept") || strings.HasPrefix(done, "skipped") {
		return display.TonePending
	}
	return display.ToneVerified
}

// kubectlFlags point kubectl at the checked context; helmFlags point helm
// at it.
func (q *quickstart) kubectlFlags() []string {
	return []string{"--kubeconfig", q.kubeconfig, "--context", q.kctx}
}

func (q *quickstart) helmFlags() []string {
	return []string{"--kubeconfig", q.kubeconfig, "--kube-context", q.kctx}
}

// writeHelmKubeconfig writes cfg to a new file only the user can read, for helm
// and kubectl.
func writeHelmKubeconfig(cfg *clientcmdapi.Config) (string, error) {
	f, err := os.CreateTemp("", "kardinal-quickstart-kubeconfig-*")
	if err != nil {
		return "", fmt.Errorf("write a kubeconfig for helm and kubectl: %w", err)
	}
	path := f.Name()
	_ = f.Close()
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("write a kubeconfig for helm and kubectl: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("write a kubeconfig for helm and kubectl: %w", err)
	}
	return path, nil
}

// deploymentReady reports whether a Deployment ns/name exists and has a
// ready replica.
func (q *quickstart) deploymentReady(ns, name string) bool {
	var d appsv1.Deployment
	if err := q.c.Get(q.ctx, types.NamespacedName{Namespace: ns, Name: name}, &d); err != nil {
		return false
	}
	return d.Status.ReadyReplicas > 0
}

// served reports whether the cluster serves gvk.
func (q *quickstart) served(gvk schema.GroupVersionKind) bool {
	_, err := q.c.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	return err == nil
}

// kro installs kro with the Graph controller, as hack/install-kro.sh does.
func (q *quickstart) kro() error {
	what := "kro " + quickstartKroVersion
	if q.served(schema.GroupVersionKind{Group: "kro.run", Version: "v1alpha1", Kind: "Graph"}) && q.deploymentReady(kroNamespace, "kro") {
		q.say(what, "already installed")
		return nil
	}
	args := append([]string{"upgrade", "--install", "kro", kroChart, "--version", quickstartKroVersion,
		"--namespace", kroNamespace, "--create-namespace",
		"--wait", "--timeout", "180s"}, q.helmFlags()...)
	for _, v := range kroHelmValues {
		args = append(args, "--set", v)
	}
	if _, err := quickstartRun(q.ctx, "helm", args...); err != nil {
		return fmt.Errorf("quickstart: install kro: %w", err)
	}
	base := "https://raw.githubusercontent.com/kubernetes-sigs/kro/v" + quickstartKroVersion + "/helm/crds/"
	for _, crd := range []string{"kro.run_graphs.yaml", "internal.kro.run_graphrevisions.yaml", "kro.run_resourcegraphdefinitions.yaml"} {
		if _, err := quickstartRun(q.ctx, "kubectl", append([]string{"apply", "--server-side", "--force-conflicts", "-f", base + crd}, q.kubectlFlags()...)...); err != nil {
			return fmt.Errorf("quickstart: kro CRDs: %w", err)
		}
	}
	if _, err := quickstartRun(q.ctx, "kubectl", append([]string{"wait", "--for=condition=Established", "crd/graphs.kro.run", "--timeout=60s"}, q.kubectlFlags()...)...); err != nil {
		return fmt.Errorf("quickstart: kro CRDs: %w", err)
	}
	q.say(what, "installed")
	return nil
}

// argoCD installs Argo CD from its release manifests.
func (q *quickstart) argoCD() error {
	what := "Argo CD " + argoCDRelease
	switch {
	case q.deploymentReady("argocd", "argocd-repo-server") && q.served(applicationSetGVK):
		q.say(what, "already installed")
		return nil
	case q.o.noArgoCD:
		q.say(what, "skipped (--no-argocd)")
		return nil
	}
	if _, err := quickstartRun(q.ctx, "kubectl", append([]string{"create", "namespace", "argocd"}, q.kubectlFlags()...)...); err != nil &&
		!strings.Contains(err.Error(), "AlreadyExists") {
		return fmt.Errorf("quickstart: install Argo CD: %w", err)
	}
	manifests := "https://raw.githubusercontent.com/argoproj/argo-cd/" + argoCDRelease + "/manifests/install.yaml"
	if _, err := quickstartRun(q.ctx, "kubectl", append([]string{"apply", "-n", "argocd", "--server-side", "--force-conflicts", "-f", manifests}, q.kubectlFlags()...)...); err != nil {
		return fmt.Errorf("quickstart: install Argo CD: %w", err)
	}
	for _, r := range []string{"deployment/argocd-repo-server", "deployment/argocd-applicationset-controller", "statefulset/argocd-application-controller"} {
		if _, err := quickstartRun(q.ctx, "kubectl", append([]string{"-n", "argocd", "rollout", "status", r, "--timeout=300s"}, q.kubectlFlags()...)...); err != nil {
			return fmt.Errorf("quickstart: Argo CD: %w", err)
		}
	}
	q.say(what, "installed")
	return nil
}

// kardinal installs the kardinal-promoter chart.
func (q *quickstart) kardinal() error {
	version := q.o.chartVersion
	if version == "" && !strings.Contains(CLIVersion, "dev") {
		version = strings.TrimPrefix(CLIVersion, "v")
	}
	what := "kardinal-promoter"
	if version != "" {
		what += " " + version
	}
	var list appsv1.DeploymentList
	if err := q.c.List(q.ctx, &list, sigs_client.InNamespace(quickstartControllerNS),
		sigs_client.MatchingLabels{"app.kubernetes.io/name": "kardinal-promoter"}); err == nil {
		for _, d := range list.Items {
			if d.Status.ReadyReplicas > 0 {
				q.say("kardinal-promoter", "already installed ("+quickstartControllerNS+"/"+d.Name+")")
				return nil
			}
		}
	}
	if err := q.scmSecret(); err != nil {
		return err
	}
	chart := q.o.chart
	if chart == "" {
		chart = quickstartChart
	}
	args := []string{"upgrade", "--install", "kardinal-promoter", chart, "--namespace", quickstartControllerNS, "--create-namespace",
		"--set", "scm.provider=github", "--set", "scm.secretRef.name=" + quickstartSCMSecret, "--wait", "--timeout", "300s"}
	if version != "" {
		args = append(args, "--version", version)
	}
	for _, s := range q.o.sets {
		args = append(args, "--set", s)
	}
	if _, err := quickstartRun(q.ctx, "helm", append(args, q.helmFlags()...)...); err != nil {
		return fmt.Errorf("quickstart: install kardinal-promoter: %w", err)
	}
	q.say(what, "installed")
	return nil
}

// scmSecret creates the controller's SCM token Secret, unlabelled (no
// Pipeline may use it), from the same token as the git Secret.
func (q *quickstart) scmSecret() error {
	what := "Secret " + quickstartControllerNS + "/" + quickstartSCMSecret
	var existing corev1.Secret
	err := q.c.Get(q.ctx, types.NamespacedName{Namespace: quickstartControllerNS, Name: quickstartSCMSecret}, &existing)
	switch {
	case err == nil:
		q.say(what, "kept")
		return nil
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("quickstart: get %s: %w", what, HumanError(err))
	}
	token, from, err := gitToken()
	if err != nil {
		return fmt.Errorf("quickstart: %w", err)
	}
	ns := &corev1.Namespace{}
	ns.Name = quickstartControllerNS
	if err := q.c.Create(q.ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("quickstart: create namespace %s: %w", quickstartControllerNS, HumanError(err))
	}
	sec := &corev1.Secret{}
	sec.Name, sec.Namespace = quickstartSCMSecret, quickstartControllerNS
	sec.StringData = map[string]string{"token": token}
	if err := q.c.Create(q.ctx, sec); err != nil {
		return fmt.Errorf("quickstart: create %s: %w", what, HumanError(err))
	}
	q.say(what, "created from "+from+" (the controller's token for PRs)")
	return nil
}

// secret creates the git Secret of the demo Pipeline (createGitSecret: kept
// when it exists, labelled referenceable when it is not).
func (q *quickstart) secret() error {
	var out strings.Builder
	if err := createGitSecret(q.ctx, q.c, quickstartNS, quickstartSecret, &out); err != nil {
		return fmt.Errorf("quickstart: %w", err)
	}
	msg := strings.TrimSpace(out.String())
	prefix := "Secret " + quickstartNS + "/" + quickstartSecret
	done := strings.TrimLeft(strings.TrimPrefix(msg, prefix), " ;")
	q.say(prefix, done)
	return nil
}

// applicationSetGVK is Argo CD's ApplicationSet.
var applicationSetGVK = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationSet"}

// apply server-side applies u for quickstart (fields it does not set, such
// as an approval mode changed later, stay as they are), reporting whether it
// changed anything.
func (q *quickstart) apply(u *unstructured.Unstructured) (bool, error) {
	key := types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()}
	version := func() (string, error) {
		var cur unstructured.Unstructured
		cur.SetGroupVersionKind(u.GroupVersionKind())
		if err := q.c.Get(q.ctx, key, &cur); err != nil {
			if apierrors.IsNotFound(err) {
				return "", nil
			}
			return "", err
		}
		return cur.GetResourceVersion(), nil
	}
	before, err := version()
	if err != nil {
		return false, fmt.Errorf("get %s %s/%s: %w", u.GetKind(), u.GetNamespace(), u.GetName(), HumanError(err))
	}
	if err := q.c.Apply(q.ctx, sigs_client.ApplyConfigurationFromUnstructured(u),
		sigs_client.FieldOwner("kardinal-quickstart"), sigs_client.ForceOwnership); err != nil {
		return false, fmt.Errorf("apply %s %s/%s: %w", u.GetKind(), u.GetNamespace(), u.GetName(), HumanError(err))
	}
	after, err := version()
	if err != nil {
		return false, fmt.Errorf("get %s %s/%s: %w", u.GetKind(), u.GetNamespace(), u.GetName(), HumanError(err))
	}
	return before != after, nil
}

// sayApplied reports an applied object.
func (q *quickstart) sayApplied(what string, changed bool) {
	if changed {
		q.say(what, "applied")
	} else {
		q.say(what, "already up to date")
	}
}

// applicationSet applies the ApplicationSet that deploys the fork's
// environments as kardinal-test-app-<env>.
func (q *quickstart) applicationSet() error {
	if !q.served(applicationSetGVK) {
		return fmt.Errorf("quickstart: Argo CD is not installed (no ApplicationSet kind is served): run quickstart without --no-argocd")
	}
	envs := make([]any, 0, len(quickstartEnvs))
	for _, e := range quickstartEnvs {
		envs = append(envs, map[string]any{"env": e})
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "ApplicationSet",
		"metadata": map[string]any{"name": quickstartAppSet, "namespace": "argocd"},
		"spec": map[string]any{
			"generators": []any{map[string]any{"list": map[string]any{"elements": envs}}},
			"template": map[string]any{
				"metadata": map[string]any{"name": quickstartAppSet + "-{{env}}"},
				"spec": map[string]any{
					"project": "default",
					"source": map[string]any{"repoURL": q.o.repo, "targetRevision": "main", "path": "environments/{{env}}",
						"kustomize": map[string]any{"namespace": quickstartAppSet + "-{{env}}"}},
					"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": quickstartAppSet + "-{{env}}"},
					"syncPolicy": map[string]any{"automated": map[string]any{"prune": true, "selfHeal": true},
						"syncOptions": []any{"CreateNamespace=true"}},
				},
			},
		},
	}}
	changed, err := q.apply(u)
	if err != nil {
		return fmt.Errorf("quickstart: %w", err)
	}
	q.sayApplied("Argo CD ApplicationSet argocd/"+quickstartAppSet+" ("+q.o.repo+")", changed)
	return nil
}

// gate applies the approval gate on prod: you approve, nobody else.
func (q *quickstart) gate() error {
	id, err := identityOf(q.ctx, q.c)
	if err != nil {
		return fmt.Errorf("quickstart: read your Kubernetes user name for the approval gate: %w", err)
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": v1alpha1.GroupVersion.String(), "kind": "PolicyGate",
		"metadata": map[string]any{"name": quickstartGate, "namespace": quickstartNS, "labels": map[string]any{
			"kardinal.io/scope": "team", "kardinal.io/applies-to": "prod", "kardinal.io/type": "gate"}},
		"spec": map[string]any{
			"expression": "true",
			"message":    "prod waits for your approval: kardinal approve " + quickstartPipeline + " prod",
			"approval":   map[string]any{"required": int64(1), "allowedUsers": []any{id.Username}, "excludeAuthor": false},
		},
	}}
	changed, err := q.apply(u)
	if err != nil {
		return fmt.Errorf("quickstart: %w", err)
	}
	q.sayApplied("PolicyGate "+quickstartNS+"/"+quickstartGate+" (prod waits for your approval, as "+id.Username+")", changed)
	return nil
}

// pipeline applies the demo Pipeline: test, uat and prod, all auto, prod
// behind the approval gate, each checked with its Argo CD Application.
func (q *quickstart) pipeline() error {
	envs := make([]any, 0, len(quickstartEnvs))
	for _, e := range quickstartEnvs {
		envs = append(envs, map[string]any{"name": e,
			"health": map[string]any{"type": "argocd", "argocd": map[string]any{"name": quickstartAppSet + "-" + e}}})
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": v1alpha1.GroupVersion.String(), "kind": "Pipeline",
		"metadata": map[string]any{"name": quickstartPipeline, "namespace": quickstartNS},
		"spec": map[string]any{
			"git":          map[string]any{"url": q.o.repo, "secretRef": map[string]any{"name": quickstartSecret}},
			"environments": envs,
		},
	}}
	changed, err := q.apply(u)
	if err != nil {
		return fmt.Errorf("quickstart: %w", err)
	}
	q.sayApplied("Pipeline "+quickstartNS+"/"+quickstartPipeline, changed)
	// The Pipeline is valid (its Secret found) before a version ships.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var p v1alpha1.Pipeline
		if err := q.c.Get(q.ctx, types.NamespacedName{Namespace: quickstartNS, Name: quickstartPipeline}, &p); err == nil {
			if c := meta.FindStatusCondition(p.Status.Conditions, "Ready"); c != nil {
				if c.Status == metav1.ConditionTrue {
					return nil
				}
				if !time.Now().Before(deadline) {
					return fmt.Errorf("quickstart: Pipeline %s/%s is not Ready (%s): %s", quickstartNS, quickstartPipeline, c.Reason, c.Message)
				}
			}
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("quickstart: Pipeline %s/%s is not Ready after 2m: is the controller running? kardinal doctor", quickstartNS, quickstartPipeline)
		}
		select {
		case <-q.ctx.Done():
			return q.ctx.Err()
		case <-time.After(shipPollInterval):
		}
	}
}

// ship ships the first version, unless the demo has one already, and follows
// it until prod waits for the approval.
func (q *quickstart) ship() error {
	var bundles v1alpha1.BundleList
	if err := q.c.List(q.ctx, &bundles, sigs_client.InNamespace(quickstartNS)); err != nil {
		return fmt.Errorf("quickstart: list bundles: %w", HumanError(err))
	}
	for _, b := range bundles.Items {
		if b.Spec.Pipeline == quickstartPipeline {
			q.say("First version", "already shipped ("+b.Name+")")
			return nil
		}
	}
	b, err := newBundle(q.ctx, q.c, quickstartNS, quickstartPipeline, createBundleOptions{Images: []string{q.o.image}, Type: "image"})
	if err != nil {
		return fmt.Errorf("quickstart: ship: %w", err)
	}
	q.say("First version", "shipped as "+b.Name)
	if q.o.noWait {
		return nil
	}
	_, _ = fmt.Fprintf(q.w, "Following %s through test, uat and prod (Ctrl-C stops following, the promotion goes on)\n",
		versionRef(createdOf(b).Version, b.Name))
	res := waitShip(q.ctx, q.c, quickstartNS, quickstartPipeline, b.Name, shipResult{createdBundle: createdOf(b)}, q.o.timeout, false)
	switch res.Result {
	case "Held", "Verified":
		_, _ = fmt.Fprintln(q.w, res.Message)
		return nil
	}
	_, _ = fmt.Fprintln(q.w, res.Message)
	return &exitError{code: res.ExitCode}
}

// next prints what to do next.
func (q *quickstart) next() {
	_, _ = fmt.Fprintf(q.w, `
Next:
  Approve prod:    kardinal approve %[1]s prod
  Watch:           kardinal status %[1]s -w
  Open the UI:     kardinal dashboard
  Ship the next:   kardinal ship %[1]s --image %[2]s --wait
`, quickstartPipeline, nextImage(q.o.image))
}

// quickstartReference is the detail of quickstart for its reference page.
const quickstartReference = `quickstart refuses, before it sends anything to a cluster, a kubeconfig
context whose name is not kind-*, k3d-*, minikube, docker-desktop or
rancher-desktop, or whose API server is not on this machine (loopback,
0.0.0.0, Docker Desktop's kubernetes.docker.internal; for minikube also its
drivers' networks 192.168.49.0/24, 192.168.39.0/24 and 192.168.59.0/24). It
also refuses to run while a variable is set that points helm, kubectl or
client-go elsewhere (HELM_KUBE*, such as HELM_KUBECONTEXT, and
KUBERNETES_MASTER), and refuses a context whose cluster has a proxy-url (the
proxy decides where requests go). There is no flag to override it, on purpose. Use
--context to pick a local context. helm and kubectl run on a kubeconfig file
with only that context, without KUBECONFIG or those variables, and kubectl
with KUBERC=off. Ctrl-C or SIGTERM stops the step that runs and removes
that file. What it
cannot see: a loopback port that tunnels to a remote cluster (kubectl
port-forward, ssh -L) looks local; do not point a local context at one.

On a local cluster it installs, each only when it is not there yet:
  kro (the version hack/install-kro.sh pins), with the Graph controller
  Argo CD (its release manifests; --no-argocd when the cluster has one)
  kardinal-promoter (the chart of this CLI's version, in kardinal-system;
    --chart, --chart-version and --set for another)
Then it applies, in default (so no command needs -n):
  the git Secret github-token, from $GITHUB_TOKEN, else gh auth token: the
    token needs contents and pull requests read and write on --repo
  an Argo CD ApplicationSet kardinal-test-app in argocd: the Applications
    kardinal-test-app-<env> sync environments/<env> of --repo
  a PolicyGate prod-approval: prod waits for your approval (you, the user
    the cluster says you are; excludeAuthor is off)
  the Pipeline demo: test, uat and prod, all auto, prod behind the gate,
    health checked with the Applications
It ships the first version (--image) unless demo has one, and follows it
until prod waits for your approval (--no-wait does not follow). It does not
change your kubeconfig. A re-run changes nothing.

Then: kardinal approve demo prod ships it to prod; kardinal dashboard opens
the UI; the Next lines show the command that ships the next version. To get
PR evidence for prod, set its approval to pr-review in the Pipeline.`
