// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Environment variables hack/e2e/components/kardinal.sh sets for suites whose
// tests install the chart themselves (the chart suite).
const (
	// EnvChart is the chart directory of the checkout under test.
	EnvChart = "KARDINAL_E2E_CHART"
	// EnvHelm is the helm binary (default: helm on PATH).
	EnvHelm = "KARDINAL_E2E_HELM"
	// EnvImage is the controller image built from the checkout and loaded
	// into the kind node (repository:tag).
	EnvImage = "KARDINAL_E2E_IMAGE"
	// EnvSCMProvider and EnvSCMAPI are the controller's --scm-provider and
	// --scm-api-url for the suite's git server.
	EnvSCMProvider = "KARDINAL_E2E_SCM_PROVIDER"
	EnvSCMAPI      = "KARDINAL_E2E_SCM_API"
)

// Values is a Helm values tree.
type Values = map[string]interface{}

// Release is a release of the chart under test that a test installed.
type Release struct {
	Name      string
	Namespace string
	// Fullname is the name of the release's Deployment, Service and
	// ServiceAccount, and the prefix of its RBAC objects (the chart's
	// kardinal-promoter.fullname).
	Fullname string
	// Values are the values the release was last installed or upgraded with,
	// base values included.
	Values Values
	// cleanNamespaces are namespaces besides Namespace whose Bundles and
	// Pipelines cleanup deletes before uninstalling (see CleansUp).
	cleanNamespaces []string
	e               *Env
}

// CleansUp adds namespaces whose Bundles and Pipelines the release's cleanup
// deletes, waiting for their Graphs to go, before it uninstalls the release:
// the app namespaces a cluster-mode release reconciles. kro removes a Graph's
// finalizer only while the Graph's RBAC exists, and the release owns it.
func (r *Release) CleansUp(namespaces ...string) {
	r.cleanNamespaces = append(r.cleanNamespaces, namespaces...)
}

// BaseChartValues are the values every test release starts from: the
// controller image built from the checkout (never pulled), the suite's git
// server as SCM provider with the git token Secret Namespace copies into each
// test namespace, and debug logs for failure diagnostics.
func BaseChartValues(t *testing.T) Values {
	t.Helper()
	image := os.Getenv(EnvImage)
	i := strings.LastIndex(image, ":")
	if i < 0 {
		t.Fatalf("%s=%q is not repository:tag; run hack/e2e/up.sh", EnvImage, image)
	}
	return Values{
		"image":    Values{"repository": image[:i], "tag": image[i+1:], "pullPolicy": "Never"},
		"logLevel": "debug",
		"scm":      Values{"provider": os.Getenv(EnvSCMProvider), "apiURL": os.Getenv(EnvSCMAPI)},
		"github":   Values{"secretRef": Values{"name": GitSecretName}},
		// A release the test installs has its own identity admission
		// policies, cluster-wide in cluster mode: they must let the suite's
		// controller and its variants write what kardinal writes.
		"admission": Values{"controllerUsernames": suiteControllers()},
	}
}

// MergeValues returns base with over merged in: maps merge key by key, and
// any other value in over replaces base's.
func MergeValues(base, over Values) Values {
	out := Values{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		bm, bok := out[k].(Values)
		om, ook := v.(Values)
		if bok && ook {
			out[k] = MergeValues(bm, om)
			continue
		}
		out[k] = v
	}
	return out
}

// ChartFullname is the chart's kardinal-promoter.fullname for release name.
func ChartFullname(release string) string {
	if strings.Contains(release, "kardinal-promoter") {
		return release
	}
	return release + "-kardinal-promoter"
}

// Helm runs helm against the test cluster and returns its combined output.
// It never fails the test.
func (e *Env) Helm(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, err := e.helm(args...)
	t.Logf("$ helm --kube-context %s %s\n%s", e.Context, strings.Join(args, " "), out)
	return out, err
}

// helm runs helm against the test cluster without logging.
func (e *Env) helm(args ...string) (string, error) {
	bin := os.Getenv(EnvHelm)
	if bin == "" {
		bin = "helm"
	}
	out, err := exec.Command(bin, append([]string{"--kube-context", e.Context}, args...)...).CombinedOutput()
	return string(out), err
}

// chartDir is the chart under test.
func chartDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv(EnvChart)
	if dir == "" {
		t.Fatalf("%s is not set; run hack/e2e/up.sh chart", EnvChart)
	}
	return dir
}

// valuesFile writes v to a file in the test's temp dir.
func valuesFile(t *testing.T, v Values) string {
	t.Helper()
	data, err := yaml.Marshal(v)
	if err != nil {
		t.Fatalf("marshal values: %v", err)
	}
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TryInstallChart runs helm install of the checkout's chart as release name
// in ns, with BaseChartValues merged with values. wait makes helm wait for
// the Deployment to be ready (3m). It returns helm's output and error and
// does not fail the test; when helm created the release, it is uninstalled
// when the test ends (see Release.cleanup).
func (e *Env) TryInstallChart(t *testing.T, name, ns string, values Values, wait bool) (*Release, string, error) {
	t.Helper()
	all := MergeValues(BaseChartValues(t), values)
	args := []string{"install", name, chartDir(t), "-n", ns, "-f", valuesFile(t, all)}
	if wait {
		args = append(args, "--wait", "--timeout", "3m")
	}
	out, err := e.Helm(t, args...)
	r := &Release{Name: name, Namespace: ns, Fullname: ChartFullname(name), Values: all, e: e}
	if _, serr := e.helm("status", name, "-n", ns); serr == nil {
		t.Cleanup(func() { r.cleanup(t) })
	}
	return r, out, err
}

// HelmTemplate runs helm template of the checkout's chart as release name in
// ns, with BaseChartValues merged with values, and returns helm's output and
// error. helm template does not contact the cluster.
func (e *Env) HelmTemplate(t *testing.T, name, ns string, values Values) (string, error) {
	t.Helper()
	all := MergeValues(BaseChartValues(t), values)
	out, err := e.helm("template", name, chartDir(t), "-n", ns, "-f", valuesFile(t, all))
	if err != nil {
		t.Logf("$ helm template %s -n %s\n%s", name, ns, out)
	}
	return out, err
}

// InstallChart is TryInstallChart with wait that fails the test when helm
// fails.
func (e *Env) InstallChart(t *testing.T, name, ns string, values Values) *Release {
	t.Helper()
	r, out, err := e.TryInstallChart(t, name, ns, values, true)
	if err != nil {
		r.dumpLogs(t)
		t.Fatalf("helm install %s: %v\n%s", name, err, out)
	}
	return r
}

// TryUpgrade runs helm upgrade with BaseChartValues merged with values
// (values from earlier installs are not reused) and returns helm's output.
func (r *Release) TryUpgrade(t *testing.T, values Values, wait bool) (string, error) {
	t.Helper()
	all := MergeValues(BaseChartValues(t), values)
	args := []string{"upgrade", r.Name, chartDir(t), "-n", r.Namespace, "-f", valuesFile(t, all)}
	if wait {
		args = append(args, "--wait", "--timeout", "3m")
	}
	out, err := r.e.Helm(t, args...)
	if err == nil {
		r.Values = all
	}
	return out, err
}

// Upgrade is TryUpgrade with wait that fails the test when helm fails.
func (r *Release) Upgrade(t *testing.T, values Values) {
	t.Helper()
	if out, err := r.TryUpgrade(t, values, true); err != nil {
		r.dumpLogs(t)
		t.Fatalf("helm upgrade %s: %v\n%s", r.Name, err, out)
	}
}

// Manifest returns helm get manifest for revision (0: the current one).
func (r *Release) Manifest(t *testing.T, revision int) string {
	t.Helper()
	args := []string{"get", "manifest", r.Name, "-n", r.Namespace}
	if revision > 0 {
		args = append(args, "--revision", fmt.Sprint(revision))
	}
	out, err := r.e.helm(args...)
	if err != nil {
		t.Fatalf("helm %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// Selector selects the release's controller Pods.
func (r *Release) Selector() labels.Selector {
	return labels.SelectorFromSet(labels.Set{
		"app.kubernetes.io/name":     "kardinal-promoter",
		"app.kubernetes.io/instance": r.Name,
	})
}

// Pods lists the release's controller Pods, terminating ones included.
func (r *Release) Pods(t *testing.T) []corev1.Pod {
	t.Helper()
	pods, err := r.listPods(context.Background())
	if err != nil {
		t.Fatalf("list %s pods: %v", r.Name, err)
	}
	return pods
}

// listPods lists the Pods the release's ReplicaSets own. Tests may start
// other Pods with the controller's labels (to be selected by its
// NetworkPolicy); those are not the controller.
func (r *Release) listPods(ctx context.Context) ([]corev1.Pod, error) {
	var pods corev1.PodList
	if err := r.e.Client.List(ctx, &pods, client.InNamespace(r.Namespace),
		client.MatchingLabelsSelector{Selector: r.Selector()}); err != nil {
		return nil, err
	}
	var out []corev1.Pod
	for _, p := range pods.Items {
		if owner := metav1.GetControllerOf(&p); owner != nil && owner.Kind == "ReplicaSet" {
			out = append(out, p)
		}
	}
	return out, nil
}

// Deployment returns the release's controller Deployment.
func (r *Release) Deployment(t *testing.T) *appsv1.Deployment {
	t.Helper()
	var d appsv1.Deployment
	if err := r.e.Client.Get(context.Background(), types.NamespacedName{Namespace: r.Namespace, Name: r.Fullname}, &d); err != nil {
		t.Fatalf("get Deployment %s/%s: %v", r.Namespace, r.Fullname, err)
	}
	return &d
}

// WaitRolledOut waits until the Deployment's current generation is rolled
// out: every replica updated, ready and available, and no old Pod left.
func (r *Release) WaitRolledOut(t *testing.T, timeout time.Duration) {
	t.Helper()
	Eventually(t, timeout, "Deployment "+r.Fullname+" rolled out", func(ctx context.Context) (bool, string) {
		var d appsv1.Deployment
		if err := r.e.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: r.Fullname}, &d); err != nil {
			return false, err.Error()
		}
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		st := d.Status
		pods, err := r.listPods(ctx)
		if err != nil {
			return false, err.Error()
		}
		done := d.Generation == st.ObservedGeneration && st.UpdatedReplicas == want &&
			st.ReadyReplicas == want && st.AvailableReplicas == want && st.Replicas == want && len(pods) == int(want)
		return done, fmt.Sprintf("updated %d, ready %d, available %d, total %d, pods %d (want %d)",
			st.UpdatedReplicas, st.ReadyReplicas, st.AvailableReplicas, st.Replicas, len(pods), want)
	})
}

// Logs returns the controller container's log of pod (previous: the
// container's last terminated run).
func (r *Release) Logs(t *testing.T, pod string, previous bool) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := r.e.Kube.CoreV1().Pods(r.Namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: "controller", Previous: previous,
	}).DoRaw(ctx)
	if err != nil {
		t.Fatalf("logs of %s/%s: %v", r.Namespace, pod, err)
	}
	return string(raw)
}

// AllLogs is the concatenated controller logs of every current Pod.
func (r *Release) AllLogs(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, p := range r.Pods(t) {
		if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
			continue
		}
		b.WriteString(r.Logs(t, p.Name, false))
	}
	return b.String()
}

// StreamLine is one line of a followed controller log (see LogStream). At is
// when the container runtime wrote it (the kubelet's log timestamp; kind
// nodes share the host's clock).
type StreamLine struct {
	At   time.Time
	Text string
}

// LogStream follows one controller Pod's log (see Release.FollowLogs).
type LogStream struct {
	mu    sync.Mutex
	lines []StreamLine
	done  chan struct{}
	// last is the latest timestamp read, and atLast how many lines had it:
	// what a reconnected stream skips.
	last   time.Time
	atLast int
}

// Lines returns the lines read so far.
func (s *LogStream) Lines() []StreamLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]StreamLine(nil), s.lines...)
}

// Find returns the first line read so far that contains substr.
func (s *LogStream) Find(substr string) (StreamLine, bool) {
	for _, l := range s.Lines() {
		if strings.Contains(l.Text, substr) {
			return l, true
		}
	}
	return StreamLine{}, false
}

// Done is closed when the log ends: the container exited or restarted, the
// Pod is gone, or the test ended.
func (s *LogStream) Done() <-chan struct{} { return s.done }

// FollowLogs streams pod's controller log from its start until the container
// exits or the test ends. The kubelet can end a follow stream while the
// container still runs (a failed container status check or file watch), so
// when the stream ends and the same container still runs, FollowLogs
// reconnects from the last line it read.
func (r *Release) FollowLogs(t *testing.T, pod string) *LogStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	pods := r.e.Kube.CoreV1().Pods(r.Namespace)
	p, err := pods.Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		cancel()
		t.Fatalf("get pod %s/%s: %v", r.Namespace, pod, err)
	}
	container := controllerContainerID(p)
	open := func(since time.Time) (io.ReadCloser, error) {
		opts := &corev1.PodLogOptions{Container: "controller", Follow: true, Timestamps: true}
		if !since.IsZero() {
			// SinceTime has second precision; follow skips what it read.
			st := metav1.NewTime(since)
			opts.SinceTime = &st
		}
		return pods.GetLogs(pod, opts).Stream(ctx)
	}
	rc, err := open(time.Time{})
	if err != nil {
		cancel()
		t.Fatalf("follow logs of %s/%s: %v", r.Namespace, pod, err)
	}
	running := func() bool {
		p, err := pods.Get(ctx, pod, metav1.GetOptions{})
		if err != nil {
			// The Pod is gone; on another error, try again.
			return !apierrors.IsNotFound(err)
		}
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name == "controller" {
				return cs.ContainerID == container && cs.State.Running != nil
			}
		}
		return false
	}
	s := &LogStream{done: make(chan struct{})}
	go s.follow(ctx, rc, open, running)
	return s
}

// controllerContainerID is the ID of pod's controller container.
func controllerContainerID(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == "controller" {
			return cs.ContainerID
		}
	}
	return ""
}

// follow reads rc, then reopens the log after the last line read for as
// long as running reports the container runs, and closes s.done when it
// stops or ctx ends.
func (s *LogStream) follow(ctx context.Context, rc io.ReadCloser, open func(since time.Time) (io.ReadCloser, error), running func() bool) {
	defer close(s.done)
	for {
		if rc != nil {
			s.read(rc)
			_ = rc.Close()
		}
		if ctx.Err() != nil || !running() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
		s.mu.Lock()
		since := s.last
		s.mu.Unlock()
		var err error
		if rc, err = open(since); err != nil {
			rc = nil
		}
	}
}

// read appends rc's lines ("<RFC3339Nano timestamp> <text>"), skipping
// those an earlier stream read: the ones before s.last and the first
// s.atLast at s.last.
func (s *LogStream) read(rc io.Reader) {
	s.mu.Lock()
	from, skip := s.last, s.atLast
	s.mu.Unlock()
	resuming := !from.IsZero()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		at, text := time.Now(), sc.Text()
		if ts, rest, ok := strings.Cut(text, " "); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				at, text = parsed, rest
			}
		}
		if resuming {
			if at.Before(from) {
				continue
			}
			if at.Equal(from) && skip > 0 {
				skip--
				continue
			}
			resuming = false
		}
		s.mu.Lock()
		switch {
		case at.After(s.last):
			s.last, s.atLast = at, 1
		case at.Equal(s.last):
			s.atLast++
		}
		s.lines = append(s.lines, StreamLine{At: at, Text: text})
		s.mu.Unlock()
	}
}

// dumpLogs writes every release Pod's log and status to the artifacts
// directory, for a failed test.
func (r *Release) dumpLogs(t *testing.T) {
	t.Helper()
	dir := filepath.Join(artifactsDir(), r.Namespace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("diagnostics: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pods, err := r.listPods(ctx)
	if err != nil {
		t.Logf("diagnostics: list pods: %v", err)
		return
	}
	for _, p := range pods {
		if st, err := yaml.Marshal(p.Status); err == nil {
			write(t, dir, "release-"+p.Name+"-status.yaml", st)
		}
		for _, prev := range []bool{false, true} {
			raw, err := r.e.Kube.CoreV1().Pods(r.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{
				Container: "controller", Previous: prev,
			}).DoRaw(ctx)
			if err != nil {
				continue
			}
			name := "logs-release-" + p.Name + ".txt"
			if prev {
				name = "logs-release-" + p.Name + "-previous.txt"
			}
			write(t, dir, name, raw)
		}
	}
	t.Logf("release %s diagnostics written to %s", r.Name, dir)
}

// cleanup uninstalls the release. It first deletes the namespace's Bundles
// and Pipelines and waits for their kro Graphs to go, so kro can remove its
// Graph finalizers while the release's Graph RBAC still exists.
func (r *Release) cleanup(t *testing.T) {
	if t.Failed() {
		r.dumpLogs(t)
	}
	if os.Getenv(EnvKeep) == "1" {
		t.Logf("keeping release %s/%s (%s=1)", r.Namespace, r.Name, EnvKeep)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	namespaces := append([]string{r.Namespace}, r.cleanNamespaces...)
	for _, ns := range namespaces {
		for _, obj := range []client.Object{&v1alpha1.Bundle{}, &v1alpha1.Pipeline{}} {
			if err := r.e.Client.DeleteAllOf(ctx, obj, client.InNamespace(ns)); err != nil && !apierrors.IsNotFound(err) {
				t.Logf("cleanup: delete %T in %s: %v", obj, ns, err)
			}
		}
	}
	for _, ns := range namespaces {
		for {
			list, err := r.e.Dynamic.Resource(GraphGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
			if err != nil || len(list.Items) == 0 {
				break
			}
			select {
			case <-ctx.Done():
				t.Logf("cleanup: %d Graphs left in %s", len(list.Items), ns)
			case <-time.After(Poll):
				continue
			}
			break
		}
	}
	if out, err := r.e.Helm(t, "uninstall", r.Name, "-n", r.Namespace, "--wait", "--timeout", "2m"); err != nil {
		t.Errorf("helm uninstall %s: %v\n%s", r.Name, err, out)
	}
}

// suiteControllers are the exact usernames of the suite's controller and its
// variants (variant-1 ... variant-MaxVariants, ControllerVariant), which a
// release a test installs must treat as kardinal controllers.
func suiteControllers() []string {
	out := []string{"system:serviceaccount:" + ControllerNamespace + ":" + ControllerServiceAccount}
	for i := 1; i <= MaxVariants; i++ {
		out = append(out, fmt.Sprintf("system:serviceaccount:%s:variant-%d", ControllerNamespace, i))
	}
	return out
}
