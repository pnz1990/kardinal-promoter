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

// doctor.go — pre-flight cluster health check for kardinal-promoter. (#578)
//
// Checks:
//   1. Controller reachable  — reads the kardinal-version ConfigMap in the controller namespace
//   2. CRDs installed        — uses discovery to find every kardinal.io/v1alpha1 resource
//   3. kro running           — looks for the kro controller pod in kro-system
//   4. kro Graph CRD         — uses discovery API to find kro.run/v1alpha1 graphs
//   5. SCM token             — reads GITHUB_TOKEN from the controller Deployment
//   6. Pipeline health       — optional via --pipeline flag

package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// defaultControllerNamespace is the Helm release namespace the docs install
// into. The controller writes its ConfigMap to its own namespace
// (POD_NAMESPACE), so any other release namespace needs --controller-namespace.
const defaultControllerNamespace = "kardinal-system"

// kroMinVersion is the kro release kardinal targets (hack/install-kro.sh
// KRO_VERSION; TestKroMinVersion_MatchesInstallScript keeps them equal).
const kroMinVersion = "0.10.0-rc.0"

// kardinalResources are the kardinal.io/v1alpha1 resources the controller
// needs (config/crd/bases; TestKardinalResources_MatchCRDs keeps them equal).
var kardinalResources = []string{
	"auditevents", "bundles", "changewindows", "metricchecks", "notificationhooks",
	"pipelines", "policygates", "promotionsteps", "prstatuses",
	"rollbackpolicies", "scheduleclocks", "subscriptions",
}

const doctorColWidth = 32

// doctorResult holds the outcome of one pre-flight check.
type doctorResult struct {
	icon   string
	label  string
	detail string
	hint   string // shown only on warn/fail
	failed bool
	warned bool
}

func newDoctorCmd() *cobra.Command {
	var pipeline, controllerNS string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run pre-flight checks to verify the cluster is correctly configured",
		Long: `Run pre-flight checks for kardinal-promoter:

  ✅ Controller reachable      kardinal-version ConfigMap in the controller namespace
  ✅ CRDs installed            every kardinal.io/v1alpha1 resource served
  ✅ kro running               kro controller pod in kro-system
  ✅ kro Graph CRD installed   kro.run/v1alpha1 graphs registered
  ✅ GitHub token              GITHUB_TOKEN set on the controller Deployment

The token check is named after the controller's --scm-provider (GitHub token,
GitLab token, Forgejo token, Gitea token, Bitbucket token or Azure DevOps
token), and is "SCM token" when doctor finds no controller Deployment.

Use --controller-namespace when kardinal-promoter is installed in a namespace
other than kardinal-system. --pipeline checks a Pipeline in the current
namespace (-n, else the kubeconfig context's namespace).

Use 'kardinal doctor' as the first troubleshooting step.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd.OutOrStdout(), pipeline, controllerNS)
		},
	}
	cmd.Flags().StringVar(&pipeline, "pipeline", "", "Also check health of this Pipeline (optional)")
	cmd.Flags().StringVar(&controllerNS, "controller-namespace", defaultControllerNamespace,
		"Namespace kardinal-promoter is installed in")
	return cmd
}

func runDoctor(w io.Writer, pipeline, controllerNS string) error {
	cfg, ns, err := buildRestConfig()
	if err != nil {
		_, _ = fmt.Fprintf(w, "\n%s Could not build kubeconfig: %v\n", doctorFail, err)
		_, _ = fmt.Fprintf(w, "Hint: ensure kubectl is configured and pointing at the correct cluster.\n")
		return fmt.Errorf("build kubeconfig: %w", err)
	}
	client, err := sigs_client.New(cfg, sigs_client.Options{Scheme: rootScheme})
	if err != nil {
		_, _ = fmt.Fprintf(w, "\n%s Could not connect to cluster: %v\n", doctorFail, err)
		return fmt.Errorf("could not connect to cluster: %w", err)
	}
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		_, _ = fmt.Fprintf(w, "\n%s Could not build discovery client: %v\n", doctorFail, err)
		return fmt.Errorf("discovery client: %w", err)
	}

	ctx := context.Background()
	results := []doctorResult{
		checkController(ctx, client, controllerNS),
		checkKardinalCRDs(disco),
		checkKroController(ctx, client),
		checkKroCRDs(disco),
		checkGitHubToken(ctx, client, controllerNS),
	}
	if pipeline != "" {
		results = append(results, checkPipelineHealth(ctx, client, ns, pipeline))
	}
	return printDoctorResults(w, results)
}

func printDoctorResults(w io.Writer, results []doctorResult) error {
	// Print header
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "kardinal-promoter pre-flight check")
	_, _ = fmt.Fprintln(w, strings.Repeat("=", 50))

	passed, warned, failed := 0, 0, 0
	for _, r := range results {
		_, _ = fmt.Fprintf(w, "%s  %-*s  %s\n", r.icon, doctorColWidth, r.label, r.detail)
		if r.hint != "" {
			indent := strings.Repeat(" ", 4+doctorColWidth+2)
			_, _ = fmt.Fprintf(w, "%s%s\n", indent, r.hint)
		}
		switch {
		case r.failed:
			failed++
		case r.warned:
			warned++
		default:
			passed++
		}
	}

	_, _ = fmt.Fprintln(w)
	summary := fmt.Sprintf("%d check(s) passed", passed)
	if warned > 0 {
		summary += fmt.Sprintf(", %d warning(s)", warned)
	}
	if failed > 0 {
		summary += fmt.Sprintf(", %d failed", failed)
	}
	_, _ = fmt.Fprintln(w, summary)

	if failed > 0 {
		return fmt.Errorf("%d pre-flight check(s) failed", failed)
	}
	return nil
}

const (
	doctorPass = "✅"
	doctorWarn = "⚠️"
	doctorFail = "❌"
)

// installCommand is the helm command that installs the chart matching a CLI
// version. A release CLI pins --version: without it helm picks the newest
// final chart, which is older than a release candidate. A dev build has no
// published chart to match, so it gets no --version.
func installCommand(cliVersion, controllerNS string) string {
	cmd := "helm upgrade --install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter"
	if v := strings.TrimPrefix(cliVersion, "v"); v != "" && !strings.Contains(v, "dev") {
		cmd += " --version " + v
	}
	return cmd + " --namespace " + controllerNS + " --create-namespace"
}

func checkController(ctx context.Context, client sigs_client.Client, controllerNS string) doctorResult {
	r := doctorResult{label: "Controller reachable"}
	var cm corev1.ConfigMap
	err := client.Get(ctx, types.NamespacedName{Namespace: controllerNS, Name: "kardinal-version"}, &cm)
	switch {
	case apierrors.IsNotFound(err):
		r.icon = doctorFail
		r.detail = fmt.Sprintf("kardinal-version ConfigMap not found in %s", controllerNS)
		r.hint = "Installed elsewhere? Use --controller-namespace. Install: " + installCommand(buildInfoVersion(), controllerNS)
		r.failed = true
		return r
	case err != nil:
		r.icon = doctorWarn
		r.detail = fmt.Sprintf("could not read kardinal-version ConfigMap in %s: %v", controllerNS, err)
		r.warned = true
		return r
	}
	ver := cm.Data["version"]
	if ver == "" {
		ver = "unknown version"
	}
	r.icon = doctorPass
	r.detail = fmt.Sprintf("kardinal-promoter %s in %s", ver, controllerNS)
	return r
}

func checkKardinalCRDs(disco discovery.DiscoveryInterface) doctorResult {
	r := doctorResult{label: "CRDs installed"}
	resources, err := disco.ServerResourcesForGroupVersion(v1alpha1.GroupVersion.String())
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.icon = doctorFail
			r.detail = "kardinal.io/v1alpha1 API group not registered"
			r.hint = "Apply CRDs: kubectl apply -f config/crd/bases/"
			r.failed = true
			return r
		}
		r.icon = doctorWarn
		r.detail = fmt.Sprintf("could not query kardinal.io/v1alpha1: %v", err)
		r.warned = true
		return r
	}
	served := make(map[string]bool, len(resources.APIResources))
	for _, res := range resources.APIResources {
		served[res.Name] = true
	}
	var missing []string
	for _, name := range kardinalResources {
		if !served[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		r.icon = doctorFail
		r.detail = "missing: " + strings.Join(missing, ", ")
		r.hint = "Apply CRDs: kubectl apply -f config/crd/bases/"
		r.failed = true
		return r
	}
	r.icon = doctorPass
	r.detail = fmt.Sprintf("all %d kardinal.io/v1alpha1 resources served", len(kardinalResources))
	return r
}

const kroInstallHint = "Install kro v" + kroMinVersion + " with the Graph feature gate: bash hack/install-kro.sh"

// kroVersion finds the running kro controller in kro-system and returns its
// image tag ("" when the image has no tag). found is false when no kro
// controller pod is running.
func kroVersion(ctx context.Context, client sigs_client.Reader) (version string, found bool, err error) {
	var pods corev1.PodList
	if err := client.List(ctx, &pods, sigs_client.InNamespace("kro-system")); err != nil {
		return "", false, fmt.Errorf("list pods in kro-system: %w", err)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range pod.Spec.Containers {
			if repo, tag := splitImageTag(c.Image); imageName(repo) == "kro" {
				return tag, true, nil
			}
		}
	}
	return "", false, nil
}

// splitImageTag splits an image reference into repository and tag. The tag
// is "" for a digest-only or untagged reference; a registry port is part of
// the repository.
func splitImageTag(image string) (repo, tag string) {
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	slash := strings.LastIndex(image, "/")
	if colon := strings.LastIndex(image, ":"); colon > slash {
		return image[:colon], image[colon+1:]
	}
	return image, ""
}

// imageName is the last path segment of an image repository.
func imageName(repo string) string {
	return repo[strings.LastIndex(repo, "/")+1:]
}

func checkKroController(ctx context.Context, client sigs_client.Client) doctorResult {
	r := doctorResult{label: "kro running"}
	ver, found, err := kroVersion(ctx, client)
	switch {
	case err != nil:
		r.icon = doctorWarn
		r.detail = "could not list pods in kro-system (no namespace or insufficient RBAC)"
		r.hint = kroInstallHint
		r.warned = true
	case !found:
		r.icon = doctorFail
		r.detail = "kro controller pod not running in kro-system"
		r.hint = kroInstallHint
		r.failed = true
	case ver == "":
		r.icon = doctorPass
		r.detail = "kro in kro-system"
	default:
		r.icon = doctorPass
		r.detail = fmt.Sprintf("kro %s in kro-system", ver)
	}
	return r
}

func checkKroCRDs(disco discovery.DiscoveryInterface) doctorResult {
	r := doctorResult{label: "kro Graph CRD installed"}
	resources, err := disco.ServerResourcesForGroupVersion("kro.run/v1alpha1")
	if err != nil {
		r.icon = doctorFail
		r.detail = "kro.run/v1alpha1 not served"
		r.hint = kroInstallHint
		r.failed = true
		return r
	}
	for _, res := range resources.APIResources {
		if res.Name == "graphs" {
			r.icon = doctorPass
			r.detail = "kro.run/v1alpha1 graphs registered"
			return r
		}
	}
	r.icon = doctorFail
	r.detail = "kro.run/v1alpha1 has no graphs resource (GraphKind feature gate off?)"
	r.hint = kroInstallHint
	r.failed = true
	return r
}

// scmTokenLabels names the token check after the controller's SCM provider
// (the --scm-provider values scm.NewProvider accepts; "" is GitHub).
var scmTokenLabels = map[string]string{
	"": "GitHub token", "github": "GitHub token", "gitlab": "GitLab token",
	"forgejo": "Forgejo token", "gitea": "Gitea token",
	"bitbucket": "Bitbucket token", "azuredevops": "Azure DevOps token",
	"bitbucket-datacenter": "Bitbucket Data Center token",
}

// scmProvider returns the SCM provider a controller container runs with, as
// the controller reads it: the last --scm-provider flag in its command and
// args, else the KARDINAL_SCM_PROVIDER env value, else "" (GitHub).
func scmProvider(c corev1.Container) string {
	provider, flagSet := "", false
	argv := append(append([]string{}, c.Command...), c.Args...)
	for i, a := range argv {
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "scm-provider" {
			continue
		}
		if !hasValue {
			if i+1 >= len(argv) {
				continue
			}
			value = argv[i+1]
		}
		provider, flagSet = value, true
	}
	if flagSet {
		return provider
	}
	for _, env := range c.Env {
		if env.Name == "KARDINAL_SCM_PROVIDER" && env.ValueFrom == nil {
			return env.Value
		}
	}
	return ""
}

// scmTokenLabel is the token check's label for a controller Deployment: the
// provider's name, or "SCM token" for a provider doctor does not know.
func scmTokenLabel(dep appsv1.Deployment) string {
	provider := ""
	for _, c := range dep.Spec.Template.Spec.Containers {
		if p := scmProvider(c); p != "" {
			provider = p
			break
		}
	}
	if label, ok := scmTokenLabels[provider]; ok {
		return label
	}
	return "SCM token"
}

// checkGitHubToken reads where the controller Deployment gets GITHUB_TOKEN:
// an inline value (helm github.token) or a Secret key (github.secretRef).
// The check is labelled with the controller's SCM provider (scmTokenLabel),
// or "SCM token" when no controller Deployment is found. The token value is
// never printed.
func checkGitHubToken(ctx context.Context, client sigs_client.Client, controllerNS string) doctorResult {
	r := doctorResult{label: "SCM token"}
	warn := func(detail, hint string) doctorResult {
		r.icon, r.detail, r.hint, r.warned = doctorWarn, detail, hint, true
		return r
	}
	const setHint = "Set it: helm upgrade kardinal-promoter ... --set github.secretRef.name=<secret> (key 'token')"

	var deps appsv1.DeploymentList
	if err := client.List(ctx, &deps, sigs_client.InNamespace(controllerNS),
		sigs_client.MatchingLabels{"app.kubernetes.io/name": "kardinal-promoter"}); err != nil {
		return warn(fmt.Sprintf("could not list Deployments in %s: %v", controllerNS, err), "")
	}
	if len(deps.Items) == 0 {
		return warn(fmt.Sprintf("no kardinal-promoter Deployment in %s", controllerNS),
			"Installed elsewhere? Use --controller-namespace.")
	}
	dep := deps.Items[0]
	r.label = scmTokenLabel(dep)
	for _, c := range dep.Spec.Template.Spec.Containers {
		for _, env := range c.Env {
			if env.Name != "GITHUB_TOKEN" {
				continue
			}
			if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
				if env.Value == "" {
					return warn("GITHUB_TOKEN is empty on Deployment "+dep.Name, setHint)
				}
				r.icon = doctorPass
				r.detail = "GITHUB_TOKEN set inline on Deployment " + dep.Name
				return r
			}
			ref := env.ValueFrom.SecretKeyRef
			var secret corev1.Secret
			err := client.Get(ctx, types.NamespacedName{Namespace: controllerNS, Name: ref.Name}, &secret)
			switch {
			case apierrors.IsNotFound(err):
				return warn(fmt.Sprintf("secret %s not found in %s", ref.Name, controllerNS),
					fmt.Sprintf("Create: kubectl create secret generic %s --namespace %s --from-literal=%s=<PAT>",
						ref.Name, controllerNS, ref.Key))
			case err != nil:
				return warn(fmt.Sprintf("could not read secret %s in %s: %v", ref.Name, controllerNS, err), "")
			case len(secret.Data[ref.Key]) == 0:
				return warn(fmt.Sprintf("secret %s has no %q key or it is empty", ref.Name, ref.Key), "")
			}
			r.icon = doctorPass
			r.detail = fmt.Sprintf("secret %s (key %s) present in %s", ref.Name, ref.Key, controllerNS)
			return r
		}
	}
	return warn("GITHUB_TOKEN is not set on Deployment "+dep.Name, setHint)
}

func checkPipelineHealth(ctx context.Context, client sigs_client.Client, ns, name string) doctorResult {
	r := doctorResult{label: fmt.Sprintf("Pipeline %q", name)}
	var p v1alpha1.Pipeline
	err := client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &p)
	switch {
	case apierrors.IsNotFound(err):
		r.icon = doctorFail
		r.detail = fmt.Sprintf("Pipeline %q not found in namespace %q", name, ns)
		r.hint = "Apply: kubectl apply -f <your-pipeline.yaml>"
		r.failed = true
		return r
	case err != nil:
		r.icon = doctorFail
		r.detail = fmt.Sprintf("get Pipeline %q in namespace %q failed: %v", name, ns, err)
		r.failed = true
		return r
	}
	// The Ready condition is the controller's verdict on the spec; the phase
	// only follows the Bundles, so a refused Pipeline can still show Ready.
	ready := meta.FindStatusCondition(p.Status.Conditions, "Ready")
	switch {
	case ready == nil || ready.ObservedGeneration < p.Generation:
		r.icon = doctorWarn
		r.warned = true
		r.detail = "not yet reconciled by the controller"
		r.hint = "Check that the controller is running and watches this namespace."
		return r
	case ready.Status != metav1.ConditionTrue:
		r.icon = doctorFail
		r.failed = true
		r.detail = fmt.Sprintf("Ready=%s (%s): %s", ready.Status, ready.Reason, ready.Message)
		r.hint = "Fix the Pipeline spec; 'kardinal validate -f <your-pipeline.yaml>' reports the same problems."
		return r
	}
	// Pipeline phases: Ready, Degraded, Promoting, Unknown (api/v1alpha1/pipeline_types.go).
	switch p.Status.Phase {
	case "Ready", "Promoting":
		r.icon = doctorPass
		r.detail = "status: " + p.Status.Phase
	case "Degraded":
		r.icon = doctorWarn
		r.warned = true
		r.detail = "status: Degraded"
	default:
		// Unknown is the phase before the first Bundle.
		r.icon = doctorPass
		r.detail = "status: Unknown (spec valid, no Bundle yet)"
	}
	return r
}
