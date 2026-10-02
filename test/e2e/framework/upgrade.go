// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

// Environment variables hack/e2e/components/kardinal-v081.sh sets for the
// upgrade suite.
const (
	// EnvV081CLI is the kardinal CLI of v0.8.1, the release the upgrade suite
	// upgrades from.
	EnvV081CLI = "KARDINAL_E2E_V081_CLI"
	// EnvUpgradePrometheusImage is the Prometheus image pulled into the node
	// for BarePrometheus.
	EnvUpgradePrometheusImage = "KARDINAL_E2E_PROMETHEUS_IMAGE"
)

// KrocodileGraphGVR is v0.8.1's Graph, reconciled by the krocodile Graph
// controller its chart bundled.
var KrocodileGraphGVR = schema.GroupVersionResource{Group: "experimental.kro.run", Version: "v1alpha1", Resource: "graphs"}

// ExistingRelease is a release of the chart that the test did not install
// (the suite's, or one the test upgrades from another chart): the test never
// uninstalls it. On failure its controller logs go to the diagnostics.
func (e *Env) ExistingRelease(t *testing.T, name, namespace string) *Release {
	t.Helper()
	r := &Release{Name: name, Namespace: namespace, Fullname: ChartFullname(name), e: e}
	t.Cleanup(func() {
		if t.Failed() {
			r.dumpLogs(t)
		}
	})
	return r
}

// ManifestObjects lists the objects of a helm manifest as "Kind namespace/name"
// ("Kind /name" for cluster-scoped ones).
func ManifestObjects(t *testing.T, manifest string) map[string]bool {
	t.Helper()
	objs := map[string]bool{}
	for _, doc := range strings.Split(manifest, "\n---") {
		var m struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
			t.Fatalf("parse manifest document: %v\n%s", err, doc)
		}
		if m.Kind == "" {
			continue
		}
		objs[m.Kind+" "+m.Metadata.Namespace+"/"+m.Metadata.Name] = true
	}
	return objs
}

// UpgradeGuide is the "Upgrading from v0.8.1" section of docs/installation.md
// in the checkout, up to the next level-2 heading.
func UpgradeGuide(t *testing.T) string {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join(RepoRoot(t), "docs", "installation.md"))
	if err != nil {
		t.Fatal(err)
	}
	const heading = "### Upgrading from v0.8.1"
	s := string(doc)
	i := strings.Index(s, heading)
	if i < 0 {
		t.Fatalf("docs/installation.md has no %q", heading)
	}
	s = s[i:]
	if j := strings.Index(s, "\n## "); j >= 0 {
		s = s[:j]
	}
	return s
}

// GuideBlock is the one bash code block of the upgrade guide that contains
// contains, dedented by its fence's indent (blocks in list items are
// indented). It fails the test unless exactly one block matches.
func GuideBlock(t *testing.T, contains string) string {
	t.Helper()
	var found []string
	var block []string
	indent, in := "", false
	for _, line := range strings.Split(UpgradeGuide(t), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		switch {
		case !in && trimmed == "```bash":
			in, indent, block = true, line[:len(line)-len(trimmed)], nil
		case in && trimmed == "```":
			in = false
			if b := strings.Join(block, "\n") + "\n"; strings.Contains(b, contains) {
				found = append(found, b)
			}
		case in:
			block = append(block, strings.TrimPrefix(line, indent))
		}
	}
	if len(found) != 1 {
		t.Fatalf("the upgrade guide has %d bash blocks containing %q, want 1:\n%s", len(found), contains, strings.Join(found, "\n"))
	}
	return found[0]
}

// GuideFix is the command in the Fix column of the upgrade guide's table of
// step 1 findings, on the row whose Finding cell is finding (as written,
// backticks included).
func GuideFix(t *testing.T, finding string) string {
	t.Helper()
	prefix := "| " + finding + " |"
	for _, line := range strings.Split(UpgradeGuide(t), "\n") {
		rest, ok := strings.CutPrefix(line, prefix)
		if !ok {
			continue
		}
		_, cmd, ok := strings.Cut(rest, "`")
		if cmd, _, ok2 := strings.Cut(cmd, "`"); ok && ok2 {
			return cmd
		}
		t.Fatalf("the Fix cell of %q has no command: %s", finding, line)
	}
	t.Fatalf("the upgrade guide has no finding row %q", prefix)
	return ""
}

// Fill replaces every placeholder in s with its value (pairs: placeholder,
// value, ...). It fails the test when s lacks a placeholder: the guide
// changed, and the test must follow it.
func Fill(t *testing.T, s string, pairs ...string) string {
	t.Helper()
	if len(pairs)%2 != 0 {
		t.Fatalf("Fill: odd number of arguments %q", pairs)
	}
	for i := 0; i < len(pairs); i += 2 {
		if !strings.Contains(s, pairs[i]) {
			t.Fatalf("%q is not in:\n%s", pairs[i], s)
		}
		s = strings.ReplaceAll(s, pairs[i], pairs[i+1])
	}
	return s
}

// RepoRoot is the checkout under test: the directory the documented commands
// that name a repository path (hack/install-kro.sh) run from.
func RepoRoot(t *testing.T) string {
	t.Helper()
	return filepath.Dir(filepath.Dir(chartDir(t)))
}

// KardinalV081 runs the v0.8.1 CLI against the test cluster (--context is
// always set) and returns its combined output. It never fails the test.
func (e *Env) KardinalV081(t *testing.T, namespace string, args ...string) (string, error) {
	t.Helper()
	cli := os.Getenv(EnvV081CLI)
	if cli == "" {
		t.Fatalf("%s is not set; run hack/e2e/up.sh upgrade", EnvV081CLI)
	}
	full := append([]string{"--context", e.Context}, args...)
	if namespace != "" {
		full = append([]string{"-n", namespace}, full...)
	}
	out, err := exec.Command(cli, full...).CombinedOutput()
	t.Logf("$ kardinal-v0.8.1 %s\n%s", strings.Join(full, " "), out)
	return string(out), err
}

// ShellRun is the outcome of one Shell script.
type ShellRun struct {
	Output string
	// Code is the script's exit code.
	Code int
}

// Shell runs script with bash (set -eo pipefail) from RepoRoot, the way a user
// runs the commands of a guide, and returns its combined output and exit code.
// It fails the test only when bash cannot run.
//
// The commands reach only the test cluster, however the script is written:
// KUBECONFIG is a copy of the test context alone, and kubectl, helm and
// kardinal are shell functions (exported to child scripts) that add
// --context or --kube-context. PATH starts with the CLI built from the
// checkout (KARDINAL_E2E_CLI) and KARDINAL_E2E_HELM, so `kardinal` and `helm`
// are the ones under test.
func (e *Env) Shell(t *testing.T, script string) ShellRun {
	t.Helper()
	kubeconfig := e.contextKubeconfig(t)
	path := os.Getenv("PATH")
	if h := os.Getenv(EnvHelm); h != "" {
		path = filepath.Dir(h) + string(os.PathListSeparator) + path
	}
	if filepath.IsAbs(e.cli) {
		path = filepath.Dir(e.cli) + string(os.PathListSeparator) + path
	}
	const prelude = `set -eo pipefail
kubectl() { command kubectl --context "$E2E_CONTEXT" "$@"; }
helm() { command helm --kube-context "$E2E_CONTEXT" "$@"; }
kardinal() { command kardinal --context "$E2E_CONTEXT" "$@"; }
export -f kubectl helm kardinal
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", prelude+script)
	cmd.Dir = RepoRoot(t)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig, "E2E_CONTEXT="+e.Context, "PATH="+path)
	out, err := cmd.CombinedOutput()
	run := ShellRun{Output: string(out)}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		run.Code = exit.ExitCode()
	case err != nil:
		t.Fatalf("bash: %v\n%s", err, out)
	}
	t.Logf("$ %s\n%s(exit %d)", strings.TrimSpace(script), out, run.Code)
	return run
}

// MustShell is Shell that fails the test on a non-zero exit.
func (e *Env) MustShell(t *testing.T, script string) string {
	t.Helper()
	run := e.Shell(t, script)
	if run.Code != 0 {
		t.Fatalf("script exited %d:\n%s\n%s", run.Code, script, run.Output)
	}
	return run.Output
}

// contextKubeconfig writes a kubeconfig with the test context alone, as the
// current context, to the test's temp dir.
func (e *Env) contextKubeconfig(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("kubectl", "config", "view", "--minify", "--flatten", "--context", e.Context).Output()
	if err != nil {
		t.Fatalf("kubectl config view --minify --context %s: %v", e.Context, err)
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// BarePrometheus runs a Prometheus with its image's default configuration in
// ns, for MetricChecks whose query needs no scraped series (vector(0)), and
// returns its in-cluster URL once it serves queries.
func (e *Env) BarePrometheus(t *testing.T, ns string) string {
	t.Helper()
	image := os.Getenv(EnvUpgradePrometheusImage)
	if image == "" {
		t.Fatalf("%s is not set; run hack/e2e/up.sh upgrade", EnvUpgradePrometheusImage)
	}
	labels := map[string]string{"app": "prometheus"}
	ctx := context.Background()
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "prometheus", Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:            "prometheus",
					Image:           image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Ports:           []corev1.ContainerPort{{ContainerPort: 9090}},
					ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{Path: "/-/ready", Port: intstr.FromInt32(9090)},
					}, PeriodSeconds: 2},
				}}},
			},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "prometheus", Namespace: ns},
		Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{
			Port: 9090, TargetPort: intstr.FromInt32(9090),
		}}},
	}
	if err := e.Client.Create(ctx, d); err != nil {
		t.Fatalf("create Prometheus Deployment: %v", err)
	}
	if err := e.Client.Create(ctx, svc); err != nil {
		t.Fatalf("create Prometheus Service: %v", err)
	}
	Eventually(t, 3*time.Minute, "Prometheus ready in "+ns, func(ctx context.Context) (bool, string) {
		var got appsv1.Deployment
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: d.Name}, &got); err != nil {
			return false, err.Error()
		}
		return got.Status.ReadyReplicas == 1, fmt.Sprintf("ready replicas %d", got.Status.ReadyReplicas)
	})
	return fmt.Sprintf("http://prometheus.%s.svc.cluster.local:9090", ns)
}
