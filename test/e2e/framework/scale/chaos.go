// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	flowcontrolv1 "k8s.io/api/flowcontrol/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// Chaos is a fault injected on a schedule until Stop: every interval (a
// random duration from Every) it runs Inject.
type Chaos struct {
	name   string
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	count  int
	errors []string
}

// Start runs inject every every() until Stop, and stops when the test ends.
func Start(t *testing.T, name string, every func() time.Duration, inject func(ctx context.Context) error) *Chaos {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Chaos{name: name, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(every()):
			}
			err := inject(ctx)
			c.mu.Lock()
			if err != nil && ctx.Err() == nil {
				c.errors = append(c.errors, err.Error())
				t.Logf("chaos %s: %v", name, err)
			} else if err == nil {
				c.count++
			}
			c.mu.Unlock()
		}
	}()
	t.Cleanup(c.Stop)
	return c
}

// Stop ends the schedule and waits for a running injection.
func (c *Chaos) Stop() {
	c.cancel()
	<-c.done
}

// Count is how many injections succeeded.
func (c *Chaos) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// Errors are the injections that failed.
func (c *Chaos) Errors() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.errors...)
}

// controllerLease is the controller's leader-election Lease (the chart's
// --leader-elect).
const controllerLease = "kardinal-promoter-leader"

// Leader returns the controller Pod holding the leader Lease.
func Leader(ctx context.Context, e *framework.Env) (string, error) {
	l, err := e.Kube.CoordinationV1().Leases(framework.ControllerNamespace).Get(ctx, controllerLease, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get Lease %s: %w", controllerLease, err)
	}
	if l.Spec.HolderIdentity == nil || *l.Spec.HolderIdentity == "" {
		return "", fmt.Errorf("leader Lease %s has no holder", controllerLease)
	}
	pod, _, _ := strings.Cut(*l.Spec.HolderIdentity, "_")
	return pod, nil
}

// KillLeader deletes the leader Pod with no grace period, as a node failure
// or an OOM kill would end it: no shutdown, no Lease release.
func KillLeader(ctx context.Context, e *framework.Env) error {
	pod, err := Leader(ctx, e)
	if err != nil {
		return err
	}
	zero := int64(0)
	err = e.Kube.CoreV1().Pods(framework.ControllerNamespace).Delete(ctx, pod, metav1.DeleteOptions{GracePeriodSeconds: &zero})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("leader %s already gone (the Lease has not moved yet)", pod)
	}
	return err
}

// RestartKro deletes the kro controller Pod with no grace period; its
// Deployment starts a new one.
func RestartKro(ctx context.Context, e *framework.Env) error {
	pods, err := e.Kube.CoreV1().Pods("kro-system").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=kro"})
	if err != nil {
		return err
	}
	zero := int64(0)
	for _, p := range pods.Items {
		if p.DeletionTimestamp != nil {
			continue
		}
		if err := e.Kube.CoreV1().Pods("kro-system").Delete(ctx, p.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// Toxiproxy drives the proxy in front of the git server
// (hack/e2e/components/toxiproxy.sh) through the API server's service proxy.
type Toxiproxy struct{ e *framework.Env }

// EnvToxiproxy is namespace/service:port of Toxiproxy's API, set by
// components/toxiproxy.sh.
const EnvToxiproxy = "KARDINAL_E2E_TOXIPROXY"

// NewToxiproxy fails the test when the suite has no Toxiproxy.
func NewToxiproxy(t *testing.T, e *framework.Env) *Toxiproxy {
	t.Helper()
	if os.Getenv(EnvToxiproxy) == "" {
		t.Fatalf("%s is not set; run hack/e2e/up.sh scale", EnvToxiproxy)
	}
	tp := &Toxiproxy{e: e}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = tp.Reset(ctx)
	})
	return tp
}

func (tp *Toxiproxy) do(ctx context.Context, method, path string, body interface{}) error {
	ns, svc, _ := strings.Cut(os.Getenv(EnvToxiproxy), "/")
	name, port, _ := strings.Cut(svc, ":")
	req := tp.e.Kube.CoreV1().RESTClient().Verb(method).Namespace(ns).Resource("services").
		Name("http:" + name + ":" + port).SubResource("proxy").Suffix(strings.TrimPrefix(path, "/"))
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		req = req.SetHeader("Content-Type", "application/json").Body(raw)
	}
	if err := req.Do(ctx).Error(); err != nil {
		return fmt.Errorf("toxiproxy %s %s: %w", method, path, err)
	}
	return nil
}

// Latency adds ms of latency (with jitter) to everything the git server
// sends back.
func (tp *Toxiproxy) Latency(ctx context.Context, ms, jitter int) error {
	return tp.do(ctx, "POST", "proxies/git/toxics", map[string]interface{}{
		"name": "latency", "type": "latency", "stream": "downstream",
		"attributes": map[string]int{"latency": ms, "jitter": jitter},
	})
}

// SetEnabled cuts (false) or restores (true) the git server: a disabled
// proxy refuses every connection and closes the open ones.
func (tp *Toxiproxy) SetEnabled(ctx context.Context, on bool) error {
	return tp.do(ctx, "POST", "proxies/git", map[string]interface{}{"enabled": on})
}

// Reset removes every toxic and enables every proxy.
func (tp *Toxiproxy) Reset(ctx context.Context) error {
	return tp.do(ctx, "POST", "reset", nil)
}

// throttleLevel names Throttle's PriorityLevelConfiguration and FlowSchema.
const throttleLevel = "kardinal-scale-throttle"

// Throttle puts the controller's ServiceAccount in an API Priority and
// Fairness priority level with one seat (nominalConcurrencyShares 1, no
// borrowing) and a short queue: the API server answers its excess requests
// 429, as a busy shared control plane does. It is undone when the test
// ends; call the returned function to undo it earlier.
func Throttle(t *testing.T, e *framework.Env) (undo func()) {
	t.Helper()
	ctx := context.Background()
	name := throttleLevel
	one, zero := int32(1), int32(0)
	pl := &flowcontrolv1.PriorityLevelConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: flowcontrolv1.PriorityLevelConfigurationSpec{
			Type: flowcontrolv1.PriorityLevelEnablementLimited,
			Limited: &flowcontrolv1.LimitedPriorityLevelConfiguration{
				NominalConcurrencyShares: &one,
				LendablePercent:          &zero,
				BorrowingLimitPercent:    &zero,
				LimitResponse: flowcontrolv1.LimitResponse{
					Type: flowcontrolv1.LimitResponseTypeQueue,
					Queuing: &flowcontrolv1.QueuingConfiguration{
						Queues: 1, HandSize: 1, QueueLengthLimit: 20,
					},
				},
			},
		},
	}
	fs := &flowcontrolv1.FlowSchema{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: flowcontrolv1.FlowSchemaSpec{
			PriorityLevelConfiguration: flowcontrolv1.PriorityLevelConfigurationReference{Name: name},
			MatchingPrecedence:         100,
			DistinguisherMethod:        &flowcontrolv1.FlowDistinguisherMethod{Type: flowcontrolv1.FlowDistinguisherMethodByUserType},
			Rules: []flowcontrolv1.PolicyRulesWithSubjects{{
				Subjects: []flowcontrolv1.Subject{{
					Kind: flowcontrolv1.SubjectKindServiceAccount,
					ServiceAccount: &flowcontrolv1.ServiceAccountSubject{
						Namespace: framework.ControllerNamespace, Name: framework.ControllerName,
					},
				}},
				ResourceRules: []flowcontrolv1.ResourcePolicyRule{{
					Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"},
					ClusterScope: true, Namespaces: []string{"*"},
				}},
				NonResourceRules: []flowcontrolv1.NonResourcePolicyRule{{Verbs: []string{"*"}, NonResourceURLs: []string{"*"}}},
			}},
		},
	}
	fc := e.Kube.FlowcontrolV1()
	// A throttle an interrupted run left behind is replaced.
	_ = fc.FlowSchemas().Delete(ctx, name, metav1.DeleteOptions{})
	_ = fc.PriorityLevelConfigurations().Delete(ctx, name, metav1.DeleteOptions{})
	if _, err := fc.PriorityLevelConfigurations().Create(ctx, pl, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create PriorityLevelConfiguration %s: %v", name, err)
	}
	if _, err := fc.FlowSchemas().Create(ctx, fs, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create FlowSchema %s: %v", name, err)
	}
	var once sync.Once
	undo = func() {
		once.Do(func() {
			_ = fc.FlowSchemas().Delete(ctx, name, metav1.DeleteOptions{})
			_ = fc.PriorityLevelConfigurations().Delete(ctx, name, metav1.DeleteOptions{})
		})
	}
	t.Cleanup(undo)
	return undo
}

// RestartController restarts every controller replica (a rollout restart)
// and waits until one leads: in-memory state (the SCM circuit breaker) is
// gone afterwards.
func RestartController(t *testing.T, e *framework.Env) {
	t.Helper()
	ctx := context.Background()
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kardinal.io/e2e-restarted-at":%q}}}}}`, time.Now().UTC().Format(time.RFC3339Nano))
	if _, err := e.Kube.AppsV1().Deployments(framework.ControllerNamespace).Patch(ctx, framework.ControllerName,
		types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		t.Errorf("restart the controller: %v", err)
		return
	}
	e.WaitControllerLeads(t)
}

// ThrottleStats reads the API server's API Priority and Fairness counters
// for the Throttle level: requests it dispatched and requests it rejected
// (429). Dispatched above zero proves the controller's requests went
// through the level.
func ThrottleStats(ctx context.Context, e *framework.Env) (dispatched, rejected float64, err error) {
	raw, err := e.Kube.CoreV1().RESTClient().Get().AbsPath("/metrics").DoRaw(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("read API server metrics: %w", err)
	}
	m, err := framework.ParseMetrics(string(raw))
	if err != nil {
		return 0, 0, err
	}
	level := map[string]string{"priority_level": throttleLevel}
	return m.Sum("apiserver_flowcontrol_dispatched_requests_total", level),
		m.Sum("apiserver_flowcontrol_rejected_requests_total", level), nil
}
