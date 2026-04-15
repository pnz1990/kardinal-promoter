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
//   1. Controller reachable  — reads kardinal-version ConfigMap
//   2. CRDs installed        — uses discovery API to list kardinal.io resources
//   3. krocodile running     — looks for graph-controller pod in kro-system
//   4. krocodile CRDs        — uses discovery API to find experimental.kro.run
//   5. GitHub token          — checks github-token secret in kardinal-system
//   6. Pipeline health       — optional via --pipeline flag

package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

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
	var pipeline string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run pre-flight checks to verify the cluster is correctly configured",
		Long: `Run pre-flight checks for kardinal-promoter:

  ✅ Controller reachable      version ConfigMap found
  ✅ CRDs installed            kardinal.io resource groups registered
  ✅ krocodile running         graph-controller pod in kro-system
  ✅ krocodile CRDs installed  experimental.kro.run groups registered
  ✅ GitHub token              github-token secret present

Use 'kardinal doctor' as the first troubleshooting step.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd.OutOrStdout(), pipeline)
		},
	}
	cmd.Flags().StringVar(&pipeline, "pipeline", "", "Also check health of this Pipeline (optional)")
	return cmd
}

func runDoctor(w io.Writer, pipeline string) error {
	// Build kubernetes client
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if globalKubeconfig != "" {
		loadingRules.ExplicitPath = globalKubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if globalContext != "" {
		overrides.CurrentContext = globalContext
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		_, _ = fmt.Fprintf(w, "\n%s Could not build kubeconfig: %v\n", doctorFail, err)
		_, _ = fmt.Fprintf(w, "Hint: ensure kubectl is configured and pointing at the correct cluster.\n")
		return fmt.Errorf("build kubeconfig: %w", err)
	}

	client, _, err := buildClient()
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
	var results []doctorResult

	// 1. Controller reachable
	results = append(results, checkController(ctx, client))

	// 2. CRDs installed (kardinal.io)
	results = append(results, checkKardinalCRDs(disco))

	// 3. krocodile running
	results = append(results, checkKrocodile(ctx, client))

	// 4. krocodile CRDs (experimental.kro.run)
	results = append(results, checkKroCRDs(disco))

	// 5. GitHub token
	results = append(results, checkGitHubToken(ctx, client))

	// 6. Pipeline health (optional)
	if pipeline != "" {
		results = append(results, checkPipelineHealth(ctx, client, pipeline))
	}

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
	doctorWarn = "⚠️ "
	doctorFail = "❌"
)

func checkController(ctx context.Context, client sigs_client.Client) doctorResult {
	r := doctorResult{label: "Controller reachable"}
	var cm corev1.ConfigMap
	if err := client.Get(ctx, types.NamespacedName{
		Namespace: "kardinal-system",
		Name:      "kardinal-version",
	}, &cm); err != nil {
		r.icon = doctorFail
		r.detail = "kardinal-version ConfigMap not found in kardinal-system"
		r.hint = "Install: helm upgrade --install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --namespace kardinal-system --create-namespace"
		r.failed = true
		return r
	}
	ver := cm.Data["version"]
	if ver == "" {
		ver = "unknown version"
	}
	r.icon = doctorPass
	r.detail = fmt.Sprintf("kardinal-promoter %s in kardinal-system", ver)
	return r
}

func checkKardinalCRDs(disco *discovery.DiscoveryClient) doctorResult {
	r := doctorResult{label: "CRDs installed"}
	groups, err := disco.ServerGroups()
	if err != nil {
		r.icon = doctorWarn
		r.detail = "could not query API groups (insufficient RBAC?)"
		r.warned = true
		return r
	}
	found := make(map[string]bool)
	for _, g := range groups.Groups {
		if g.Name == "kardinal.io" {
			for _, v := range g.Versions {
				_ = v
				found["kardinal.io"] = true
			}
		}
	}
	if !found["kardinal.io"] {
		r.icon = doctorFail
		r.detail = "kardinal.io API group not registered"
		r.hint = "Apply CRDs: kubectl apply -f config/crd/bases/"
		r.failed = true
		return r
	}
	r.icon = doctorPass
	r.detail = "pipelines, bundles, promotionsteps, policygates, prstatuses"
	return r
}

func checkKrocodile(ctx context.Context, client sigs_client.Client) doctorResult {
	r := doctorResult{label: "krocodile running"}
	var podList corev1.PodList
	if err := client.List(ctx, &podList, sigs_client.InNamespace("kro-system")); err != nil {
		r.icon = doctorWarn
		r.detail = "could not list pods in kro-system (no namespace or insufficient RBAC)"
		r.hint = "Install: KROCODILE_COMMIT=948ad6c bash hack/install-krocodile.sh"
		r.warned = true
		return r
	}
	for _, pod := range podList.Items {
		name := pod.Name
		if strings.Contains(name, "graph-controller") || strings.Contains(name, "kro") {
			if pod.Status.Phase == corev1.PodRunning {
				ver := ""
				for _, c := range pod.Spec.Containers {
					if idx := strings.LastIndex(c.Image, ":"); idx >= 0 {
						ver = c.Image[idx+1:]
					}
				}
				if ver != "" {
					r.detail = fmt.Sprintf("graph-controller %s in kro-system", ver)
				} else {
					r.detail = "graph-controller in kro-system"
				}
				r.icon = doctorPass
				return r
			}
		}
	}
	r.icon = doctorFail
	r.detail = "graph-controller pod not running in kro-system"
	r.hint = "Install: KROCODILE_COMMIT=948ad6c bash hack/install-krocodile.sh"
	r.failed = true
	return r
}

func checkKroCRDs(disco *discovery.DiscoveryClient) doctorResult {
	r := doctorResult{label: "krocodile CRDs installed"}
	groups, err := disco.ServerGroups()
	if err != nil {
		r.icon = doctorWarn
		r.detail = "could not query API groups"
		r.warned = true
		return r
	}
	for _, g := range groups.Groups {
		if strings.Contains(g.Name, "kro.run") {
			r.icon = doctorPass
			r.detail = fmt.Sprintf("group %s registered", g.Name)
			return r
		}
	}
	r.icon = doctorFail
	r.detail = "no *.kro.run API group found"
	r.hint = "Install krocodile: KROCODILE_COMMIT=948ad6c bash hack/install-krocodile.sh"
	r.failed = true
	return r
}

func checkGitHubToken(ctx context.Context, client sigs_client.Client) doctorResult {
	r := doctorResult{label: "GitHub token"}
	var secret corev1.Secret
	if err := client.Get(ctx, types.NamespacedName{
		Namespace: "kardinal-system",
		Name:      "github-token",
	}, &secret); err != nil {
		r.icon = doctorWarn
		r.detail = "secret github-token not found in kardinal-system"
		r.hint = "Create: kubectl create secret generic github-token --namespace kardinal-system --from-literal=token=<PAT>"
		r.warned = true
		return r
	}
	if len(secret.Data["token"]) == 0 {
		r.icon = doctorWarn
		r.detail = "secret exists but 'token' key is empty"
		r.hint = "Recreate: kubectl create secret generic github-token -n kardinal-system --from-literal=token=<PAT> --dry-run=client -o yaml | kubectl apply -f -"
		r.warned = true
		return r
	}
	r.icon = doctorPass
	r.detail = "secret github-token present in kardinal-system"
	return r
}

func checkPipelineHealth(ctx context.Context, client sigs_client.Client, name string) doctorResult {
	r := doctorResult{label: fmt.Sprintf("Pipeline %q", name)}
	ns := globalNamespace
	if ns == "" {
		ns = "default"
	}
	var p v1alpha1.Pipeline
	if err := client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &p); err != nil {
		r.icon = doctorFail
		r.detail = fmt.Sprintf("Pipeline %q not found in namespace %q", name, ns)
		r.hint = "Apply: kubectl apply -f <your-pipeline.yaml>"
		r.failed = true
		return r
	}
	phase := p.Status.Phase
	if phase == "" {
		phase = "Pending"
	}
	switch p.Status.Phase {
	case "Healthy", "Verified":
		r.icon = doctorPass
	case "Degraded", "Failed":
		r.icon = doctorWarn
		r.warned = true
	default:
		r.icon = doctorPass
	}
	r.detail = fmt.Sprintf("status: %s", phase)
	return r
}
