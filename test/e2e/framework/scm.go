// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The controller as hack/e2e/components/kardinal.sh installs it.
const (
	// ControllerName is the controller's Deployment and Service.
	ControllerName = "kardinal-promoter"
	// ControllerContainer is the controller's container in its pod.
	ControllerContainer = "controller"
	// controllerLease is the leader election Lease. Only the leader runs the
	// reconcilers and the SCM credential watcher.
	controllerLease = "kardinal-promoter-leader"
	// rolloutTimeout covers a new pod's start plus the wait for the old
	// leader's lease to expire: the controller does not release it on exit.
	rolloutTimeout = 3 * time.Minute
)

// LogLine is one JSON line of the controller log.
type LogLine struct {
	// At is the kubelet's timestamp of the line (nanosecond precision; the
	// line's own time field has seconds only).
	At      time.Time
	Pod     string
	Level   string
	Message string
	// Fields holds the line's other fields; numbers are json.Number.
	Fields map[string]interface{}
}

// Str returns field key as a string ("" when absent).
func (l LogLine) Str(key string) string {
	switch v := l.Fields[key].(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func (l LogLine) String() string {
	keys := make([]string, 0, len(l.Fields))
	for k := range l.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %q", l.Level, l.At.UTC().Format(time.RFC3339Nano), l.Message)
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%v", k, l.Fields[k])
	}
	return b.String()
}

// LogReader reads the controller's log lines written after a mark, from
// every controller pod, a little more on each Next.
type LogReader struct {
	e        *Env
	selector string
	since    time.Time
	seen     map[string]time.Time // per pod: the last line read
}

// ControllerLog reads the lines the kubelet stamps after since. Use
// time.Now() just before the action whose log lines a test waits for: the
// kind node shares the test runner's clock.
func (e *Env) ControllerLog(since time.Time) *LogReader {
	return &LogReader{e: e, selector: ControllerSelector, since: since, seen: map[string]time.Time{}}
}

// VariantLog reads the log of controller variant v as ControllerLog reads the
// controller's.
func (e *Env) VariantLog(v *Variant, since time.Time) *LogReader {
	return &LogReader{e: e, selector: variantLabel + "=" + v.Name, since: since, seen: map[string]time.Time{}}
}

// Next returns the lines written since the previous call, oldest first.
// Pods whose container is not running are skipped.
func (r *LogReader) Next(ctx context.Context) ([]LogLine, error) {
	pods, err := r.e.Kube.CoreV1().Pods(ControllerNamespace).List(ctx, metav1.ListOptions{LabelSelector: r.selector})
	if err != nil {
		return nil, err
	}
	var out []LogLine
	for _, p := range pods.Items {
		from, ok := r.seen[p.Name]
		if !ok {
			from = r.since
		}
		lines, last, err := r.e.podLog(ctx, p.Name, from)
		if err != nil {
			if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil {
				continue
			}
			return nil, fmt.Errorf("logs of %s: %w", p.Name, err)
		}
		if last.After(from) {
			r.seen[p.Name] = last
		}
		out = append(out, lines...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// podLog returns pod's controller log lines stamped after from, and the
// timestamp of the last line read.
func (e *Env) podLog(ctx context.Context, pod string, from time.Time) ([]LogLine, time.Time, error) {
	// SinceTime has seconds precision; the exact cut is made below.
	sinceTime := metav1.NewTime(from.Add(-time.Second))
	stream, err := e.Kube.CoreV1().Pods(ControllerNamespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: ControllerContainer, Timestamps: true, SinceTime: &sinceTime,
	}).Stream(ctx)
	if err != nil {
		return nil, from, err
	}
	defer func() { _ = stream.Close() }()
	var out []LogLine
	last := from
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		ts, rest, ok := strings.Cut(sc.Text(), " ")
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil || !at.After(from) {
			continue
		}
		last = at
		dec := json.NewDecoder(strings.NewReader(rest))
		dec.UseNumber()
		var fields map[string]interface{}
		if dec.Decode(&fields) != nil {
			continue // not a zerolog line (klog, panics)
		}
		l := LogLine{At: at, Pod: pod, Fields: fields}
		l.Level, _ = fields["level"].(string)
		l.Message, _ = fields["message"].(string)
		delete(fields, "level")
		delete(fields, "message")
		delete(fields, "time")
		out = append(out, l)
	}
	return out, last, sc.Err()
}

// WaitControllerLog waits until a controller log line written after since
// matches match, and returns it.
func (e *Env) WaitControllerLog(t *testing.T, since time.Time, timeout time.Duration, what string,
	match func(LogLine) bool) LogLine {
	t.Helper()
	return e.ControllerLog(since).Wait(t, timeout, "controller log: "+what, match)
}

// Wait waits until a line read from now on matches match, and returns it.
func (r *LogReader) Wait(t *testing.T, timeout time.Duration, what string, match func(LogLine) bool) LogLine {
	t.Helper()
	var found LogLine
	read := 0
	Eventually(t, timeout, what, func(ctx context.Context) (bool, string) {
		lines, err := r.Next(ctx)
		if err != nil {
			return false, err.Error()
		}
		read += len(lines)
		for _, l := range lines {
			if match(l) {
				found = l
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d lines since %s, none matches", read, r.since.UTC().Format(time.RFC3339Nano))
	})
	t.Logf("%s: %s", what, found)
	return found
}

// Lines reads the lines written since the previous call that match match.
func (r *LogReader) Lines(t *testing.T, match func(LogLine) bool) []LogLine {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	lines, err := r.Next(ctx)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var hits []LogLine
	for _, l := range lines {
		if match(l) {
			hits = append(hits, l)
		}
	}
	return hits
}

// ControllerLogLines returns the controller log lines written after since
// that match match.
func (e *Env) ControllerLogLines(t *testing.T, since time.Time, match func(LogLine) bool) []LogLine {
	t.Helper()
	return e.ControllerLog(since).Lines(t, match)
}

// LogMessage matches lines whose message starts with prefix and whose
// fields have the given string values.
func LogMessage(prefix string, fields ...string) func(LogLine) bool {
	return func(l LogLine) bool {
		if !strings.HasPrefix(l.Message, prefix) {
			return false
		}
		for i := 0; i+1 < len(fields); i += 2 {
			if l.Str(fields[i]) != fields[i+1] {
				return false
			}
		}
		return true
	}
}

// WebhookSecret is the secret the controller verifies SCM webhooks with.
func WebhookSecret(t *testing.T) string {
	t.Helper()
	s := os.Getenv(EnvWebhookSecret)
	if s == "" {
		t.Fatalf("%s is not set; hack/e2e/up.sh sets it for every git server suite", EnvWebhookSecret)
	}
	return s
}

// HMACHex is the hex HMAC-SHA256 of body with secret, the signature GitHub
// (with a "sha256=" prefix), Gitea and Forgejo send.
func HMACHex(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// PostSCMWebhook posts body to the controller's /webhook/scm through the
// API server's service proxy, as a git server in the cluster would, and
// returns the HTTP status.
func (e *Env) PostSCMWebhook(t *testing.T, headers map[string]string, body []byte) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := e.Kube.CoreV1().RESTClient().Post().Namespace(ControllerNamespace).
		Resource("services").Name(ControllerName+":webhook").SubResource("proxy").
		Suffix("webhook", "scm").SetHeader("Content-Type", "application/json").Body(body)
	for k, v := range headers {
		req = req.SetHeader(k, v)
	}
	var code int
	res := req.Do(ctx).StatusCode(&code)
	if code == 0 {
		code = proxiedStatus(res.Error())
	}
	if code == 0 {
		t.Fatalf("post SCM webhook: %v", res.Error())
	}
	return code
}

// proxiedStatus is the HTTP status in err, an error client-go made from a
// proxied response, or 0. client-go keeps the status only in the error when
// it cannot decode the body: the controller answers 401 and 400 in
// text/plain.
func proxiedStatus(err error) int {
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		return int(st.Status().Code)
	}
	return 0
}

// WebhookHealth is GET /webhook/scm/health.
type WebhookHealth struct {
	Status            string `json:"status"`
	WebhookConfigured bool   `json:"webhookConfigured"`
	EventsProcessed   int64  `json:"eventsProcessed"`
	MergedPREvents    int64  `json:"mergedPREvents"`
}

// SCMWebhookHealth reads the controller's webhook health endpoint.
func (e *Env) SCMWebhookHealth(t *testing.T) WebhookHealth {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := e.Kube.CoreV1().RESTClient().Get().Namespace(ControllerNamespace).
		Resource("services").Name(ControllerName+":webhook").SubResource("proxy").
		Suffix("webhook", "scm", "health").DoRaw(ctx)
	if err != nil {
		t.Fatalf("GET /webhook/scm/health: %v", err)
	}
	var h WebhookHealth
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatalf("decode /webhook/scm/health %q: %v", raw, err)
	}
	return h
}

// WebhookHealthAt reads GET /webhook/scm/health of the controller whose
// webhook port the host reaches at base (a Variant's URL). The endpoint
// answers JSON.
func WebhookHealthAt(t *testing.T, base string) WebhookHealth {
	t.Helper()
	res := HTTP(t, http.MethodGet, base+"/webhook/scm/health", nil, nil)
	if res.Status != http.StatusOK || res.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("GET %s/webhook/scm/health: HTTP %d %s %s", base, res.Status, res.Header.Get("Content-Type"), clip(res.Body))
	}
	var h WebhookHealth
	if err := json.Unmarshal([]byte(res.Body), &h); err != nil {
		t.Fatalf("decode /webhook/scm/health %q: %v", res.Body, err)
	}
	return h
}

// PatchController changes the controller Deployment's pod template with
// edit and waits until a new pod leads. The returned func restores the
// template and waits again; it also runs when the test ends. Tests that call
// it must not be parallel: every other test sees the changed controller.
func (e *Env) PatchController(t *testing.T, edit func(*corev1.PodSpec)) (restore func()) {
	t.Helper()
	var orig *corev1.PodTemplateSpec
	e.updateController(t, func(d *appsv1.Deployment) {
		if orig == nil {
			orig = d.Spec.Template.DeepCopy()
		}
		edit(&d.Spec.Template.Spec)
		if d.Spec.Template.Annotations == nil {
			d.Spec.Template.Annotations = map[string]string{}
		}
		// A new pod even when edit changes nothing the pod sees.
		d.Spec.Template.Annotations["kardinal.io/e2e-patched-at"] = time.Now().UTC().Format(time.RFC3339Nano)
	})
	var once sync.Once
	restore = func() {
		once.Do(func() {
			e.updateController(t, func(d *appsv1.Deployment) { d.Spec.Template = *orig.DeepCopy() })
		})
	}
	t.Cleanup(restore)
	return restore
}

// EditController changes the controller's pod template again after a
// PatchController, whose restore puts the original back, and waits until a
// new pod leads.
func (e *Env) EditController(t *testing.T, edit func(*corev1.PodSpec)) {
	t.Helper()
	e.updateController(t, func(d *appsv1.Deployment) {
		edit(&d.Spec.Template.Spec)
		if d.Spec.Template.Annotations == nil {
			d.Spec.Template.Annotations = map[string]string{}
		}
		d.Spec.Template.Annotations["kardinal.io/e2e-patched-at"] = time.Now().UTC().Format(time.RFC3339Nano)
	})
}

// RestartController restarts the controller, as kubectl rollout restart
// does, and waits until the new pod leads.
func (e *Env) RestartController(t *testing.T) {
	t.Helper()
	e.updateController(t, func(d *appsv1.Deployment) {
		if d.Spec.Template.Annotations == nil {
			d.Spec.Template.Annotations = map[string]string{}
		}
		d.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	})
}

// StopController scales the controller Deployment to zero and waits until no
// controller pod is left. The returned func scales it back and waits until
// the new pod leads; it also runs when the test ends. Tests that call it must
// not be parallel: no other test's promotion moves while it is stopped.
func (e *Env) StopController(t *testing.T) (start func()) {
	t.Helper()
	ctx := context.Background()
	key := types.NamespacedName{Namespace: ControllerNamespace, Name: ControllerName}
	var d appsv1.Deployment
	if err := e.Client.Get(ctx, key, &d); err != nil {
		t.Fatalf("get Deployment %s: %v", key, err)
	}
	replicas := int32(1)
	if d.Spec.Replicas != nil {
		replicas = *d.Spec.Replicas
	}
	scale := func(n int32) {
		t.Helper()
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var cur appsv1.Deployment
			if err := e.Client.Get(ctx, key, &cur); err != nil {
				return err
			}
			cur.Spec.Replicas = &n
			return e.Client.Update(ctx, &cur)
		})
		if err != nil {
			t.Fatalf("scale Deployment %s to %d: %v", key, n, err)
		}
	}
	var once sync.Once
	start = func() {
		once.Do(func() {
			scale(replicas)
			e.WaitControllerLeads(t)
		})
	}
	t.Cleanup(start)
	scale(0)
	Eventually(t, rolloutTimeout, "no controller pod is left", func(ctx context.Context) (bool, string) {
		var pods corev1.PodList
		if err := e.Client.List(ctx, &pods, client.InNamespace(ControllerNamespace),
			client.MatchingLabels{"app.kubernetes.io/name": ControllerName}); err != nil {
			return false, err.Error()
		}
		var names []string
		for _, p := range pods.Items {
			names = append(names, p.Name)
		}
		return len(names) == 0, fmt.Sprintf("pods %v", names)
	})
	return start
}

func (e *Env) updateController(t *testing.T, edit func(*appsv1.Deployment)) {
	t.Helper()
	ctx := context.Background()
	key := types.NamespacedName{Namespace: ControllerNamespace, Name: ControllerName}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var d appsv1.Deployment
		if err := e.Client.Get(ctx, key, &d); err != nil {
			return err
		}
		edit(&d)
		return e.Client.Update(ctx, &d)
	})
	if err != nil {
		t.Fatalf("update Deployment %s: %v", key, err)
	}
	e.WaitControllerLeads(t)
}

// WaitControllerLeads waits until the controller Deployment is rolled out,
// no old pod is left, and the new pod holds the leader Lease. It returns the
// pod's name.
func (e *Env) WaitControllerLeads(t *testing.T) string {
	t.Helper()
	key := types.NamespacedName{Namespace: ControllerNamespace, Name: ControllerName}
	var leader string
	Eventually(t, rolloutTimeout, "the controller rollout and leader election", func(ctx context.Context) (bool, string) {
		var d appsv1.Deployment
		if err := e.Client.Get(ctx, key, &d); err != nil {
			return false, err.Error()
		}
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		s := d.Status
		if s.ObservedGeneration < d.Generation || s.Replicas != want || s.UpdatedReplicas != want ||
			s.ReadyReplicas != want || s.AvailableReplicas != want {
			return false, fmt.Sprintf("generation %d observed %d, replicas %d updated %d ready %d available %d (want %d)",
				d.Generation, s.ObservedGeneration, s.Replicas, s.UpdatedReplicas, s.ReadyReplicas, s.AvailableReplicas, want)
		}
		var pods corev1.PodList
		if err := e.Client.List(ctx, &pods, client.InNamespace(ControllerNamespace),
			client.MatchingLabels{"app.kubernetes.io/name": ControllerName}); err != nil {
			return false, err.Error()
		}
		if len(pods.Items) != int(want) {
			return false, fmt.Sprintf("%d controller pods, want %d", len(pods.Items), want)
		}
		var lease struct {
			Spec struct {
				HolderIdentity string `json:"holderIdentity"`
			} `json:"spec"`
		}
		raw, err := e.Kube.CoordinationV1().RESTClient().Get().Namespace(ControllerNamespace).
			Resource("leases").Name(controllerLease).DoRaw(ctx)
		if err != nil {
			return false, "lease: " + err.Error()
		}
		if err := json.Unmarshal(raw, &lease); err != nil {
			return false, "lease: " + err.Error()
		}
		for _, p := range pods.Items {
			if p.DeletionTimestamp == nil && strings.HasPrefix(lease.Spec.HolderIdentity, p.Name+"_") {
				leader = p.Name
				return true, ""
			}
		}
		return false, "lease held by " + lease.Spec.HolderIdentity
	})
	t.Logf("controller pod %s leads", leader)
	return leader
}

// ControllerPod returns the running controller pod (there is one replica).
func (e *Env) ControllerPod(t *testing.T) corev1.Pod {
	t.Helper()
	var pods corev1.PodList
	if err := e.Client.List(context.Background(), &pods, client.InNamespace(ControllerNamespace),
		client.MatchingLabels{"app.kubernetes.io/name": ControllerName}); err != nil {
		t.Fatalf("list controller pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("%d controller pods, want 1", len(pods.Items))
	}
	return pods.Items[0]
}

// SetEnv sets env var name of the controller container to value, or removes
// it when value is nil.
func SetEnv(spec *corev1.PodSpec, name string, value *corev1.EnvVar) {
	for i := range spec.Containers {
		c := &spec.Containers[i]
		if c.Name != ControllerContainer {
			continue
		}
		out := c.Env[:0]
		for _, ev := range c.Env {
			if ev.Name != name {
				out = append(out, ev)
			}
		}
		if value != nil {
			out = append(out, *value)
		}
		c.Env = out
	}
}

// SetArg sets the controller's --name flag to value, or removes it when
// value is "".
func SetArg(spec *corev1.PodSpec, name, value string) {
	for i := range spec.Containers {
		c := &spec.Containers[i]
		if c.Name != ControllerContainer {
			continue
		}
		out := c.Args[:0]
		for _, a := range c.Args {
			if a != "--"+name && !strings.HasPrefix(a, "--"+name+"=") {
				out = append(out, a)
			}
		}
		if value != "" {
			out = append(out, "--"+name+"="+value)
		}
		c.Args = out
	}
}

// SetSecretValue sets key of Secret ns/name to value. The returned func
// restores the old value; it also runs when the test ends.
func (e *Env) SetSecretValue(t *testing.T, ns, name, key string, value []byte) (restore func()) {
	t.Helper()
	var old []byte
	had := false
	first := true
	set := func(v []byte, remove bool) error {
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var s corev1.Secret
			if err := e.Client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
				return err
			}
			if first {
				old, had = s.Data[key]
				first = false
			}
			if s.Data == nil {
				s.Data = map[string][]byte{}
			}
			if remove {
				delete(s.Data, key)
			} else {
				s.Data[key] = v
			}
			return e.Client.Update(context.Background(), &s)
		})
	}
	if err := set(value, false); err != nil {
		t.Fatalf("set Secret %s/%s key %s: %v", ns, name, key, err)
	}
	var once sync.Once
	restore = func() {
		once.Do(func() {
			if err := set(old, !had); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("restore Secret %s/%s key %s: %v", ns, name, key, err)
			}
		})
	}
	t.Cleanup(restore)
	return restore
}

// PRStatusOf returns the PRStatus the step's spec.prStatusRef names.
func (e *Env) PRStatusOf(ctx context.Context, ps *v1alpha1.PromotionStep) (*v1alpha1.PRStatus, error) {
	if ps.Spec.PRStatusRef == "" {
		return nil, fmt.Errorf("step %s has no spec.prStatusRef", ps.Name)
	}
	var prs v1alpha1.PRStatus
	err := e.Client.Get(ctx, types.NamespacedName{Namespace: ps.Namespace, Name: ps.Spec.PRStatusRef}, &prs)
	return &prs, err
}

// WaitPRStatus waits until the PRStatus of the Bundle's step for env
// satisfies match, and returns it.
func (e *Env) WaitPRStatus(t *testing.T, ns, pipeline, bundle, env string, timeout time.Duration, what string,
	match func(*v1alpha1.PRStatus) bool) *v1alpha1.PRStatus {
	t.Helper()
	var got *v1alpha1.PRStatus
	Eventually(t, timeout, fmt.Sprintf("PRStatus of %s/%s %s", bundle, env, what), func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, ns, pipeline, bundle, env)
		if err != nil || !ok {
			return false, fmt.Sprintf("step: ok=%v err=%v", ok, err)
		}
		prs, err := e.PRStatusOf(ctx, ps)
		if err != nil {
			return false, err.Error()
		}
		got = prs
		return match(prs), DescribePRStatus(prs)
	})
	return got
}

// DescribePRStatus is a one-line summary of a PRStatus.
func DescribePRStatus(p *v1alpha1.PRStatus) string {
	return fmt.Sprintf("%s spec{pr=%d repo=%q} status{open=%v merged=%v approved=%v approvals=%d mergeCommit=%q closedFinal=%v pollError=%q}",
		p.Name, p.Spec.PRNumber, p.Spec.Repo, p.Status.Open, p.Status.Merged, p.Status.Approved,
		p.Status.ApprovalCount, p.Status.MergeCommitSHA, p.Status.ClosedFinal, p.Status.PollError)
}

// RewindToOpenPR puts a step that waits for its PR back to the open-pr
// step, with the PR forgotten, as if the controller crashed after opening the
// PR and before saving it. The step engine then runs open-pr again. It
// returns the index of open-pr in the step's sequence.
func (e *Env) RewindToOpenPR(t *testing.T, ps *v1alpha1.PromotionStep) int {
	t.Helper()
	idx := -1
	for i, s := range ps.Status.Steps {
		if s.Name == "open-pr" {
			idx = i
		}
	}
	if idx < 0 || idx+1 >= len(ps.Status.Steps) {
		t.Fatalf("step %s has no open-pr followed by another step: %+v", ps.Name, ps.Status.Steps)
	}
	type op struct {
		Op    string      `json:"op"`
		Path  string      `json:"path"`
		Value interface{} `json:"value,omitempty"`
	}
	ops := []op{
		{Op: "test", Path: "/status/state", Value: "WaitingForMerge"},
		{Op: "replace", Path: "/status/state", Value: "Promoting"},
		{Op: "replace", Path: "/status/currentStepIndex", Value: idx},
		{Op: "replace", Path: "/status/message", Value: "e2e: rewound to open-pr"},
		{Op: "replace", Path: fmt.Sprintf("/status/steps/%d/state", idx), Value: "Pending"},
		{Op: "replace", Path: fmt.Sprintf("/status/steps/%d/state", idx+1), Value: "Pending"},
	}
	if ps.Status.Steps[idx].CompletedAt != nil {
		ops = append(ops, op{Op: "remove", Path: fmt.Sprintf("/status/steps/%d/completedAt", idx)})
	}
	if ps.Status.Steps[idx+1].StartedAt != nil {
		ops = append(ops, op{Op: "remove", Path: fmt.Sprintf("/status/steps/%d/startedAt", idx+1)})
	}
	for _, k := range []string{"prURL", "prNumber"} {
		if _, ok := ps.Status.Outputs[k]; ok {
			ops = append(ops, op{Op: "remove", Path: "/status/outputs/" + k})
		}
	}
	if ps.Status.PRURL != "" {
		ops = append(ops, op{Op: "remove", Path: "/status/prURL"})
	}
	raw, err := json.Marshal(ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Client.Status().Patch(context.Background(), ps, client.RawPatch(types.JSONPatchType, raw)); err != nil {
		t.Fatalf("rewind step %s to open-pr: %v", ps.Name, err)
	}
	t.Logf("rewound step %s to open-pr (index %d)", ps.Name, idx)
	return idx
}

// Brancher returns the suite's git server as a gitserver.Brancher.
func (e *Env) Brancher(t *testing.T) gitserver.Brancher {
	t.Helper()
	b, ok := e.Git.(gitserver.Brancher)
	if !ok {
		t.Fatalf("%s git server can't make branches", e.Git.Kind())
	}
	return b
}

// GitUsers returns the suite's git server as a gitserver.Users.
func (e *Env) GitUsers(t *testing.T) gitserver.Users {
	t.Helper()
	u, ok := e.Git.(gitserver.Users)
	if !ok {
		t.Fatalf("%s git server can't make users", e.Git.Kind())
	}
	return u
}

// GitUser creates a git server user with a token limited to scopes, deletes
// it when the test ends, and returns the token.
func (e *Env) GitUser(t *testing.T, name string, scopes []string) string {
	t.Helper()
	u := e.GitUsers(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tok, err := u.CreateUser(ctx, name, scopes)
	if err != nil {
		t.Fatalf("create %s user %s: %v", e.Git.Kind(), name, err)
	}
	if tok == "" {
		t.Fatalf("create %s user %s: no token", e.Git.Kind(), name)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := u.DeleteUser(ctx, name); err != nil {
			t.Errorf("delete user %s: %v", name, err)
		}
	})
	return tok
}

// GitBranch creates branch from from on repo and deletes it when the test
// ends (before the repo, which was registered earlier).
func (e *Env) GitBranch(t *testing.T, repo gitserver.Repo, branch, from string) {
	t.Helper()
	b := e.Brancher(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := b.CreateBranch(ctx, repo, branch, from); err != nil {
		t.Fatalf("create branch %s: %v", branch, err)
	}
	t.Cleanup(func() {
		if os.Getenv(EnvKeep) == "1" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := b.DeleteBranch(ctx, repo, branch); err != nil {
			t.Errorf("delete branch %s: %v", branch, err)
		}
	})
}

// JSONBody marshals v, failing the test on error.
func JSONBody(t *testing.T, v interface{}) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(buf.Bytes())
}
