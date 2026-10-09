// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// This file holds the harness the flow suites (graph, bundle, pipeline, step,
// audit, install, examples) use on top of env.go and wait.go.

// GraphGVR is kro's Graph, one per Bundle.
var GraphGVR = schema.GroupVersionResource{Group: "kro.run", Version: "v1alpha1", Resource: "graphs"}

// PromotionStepGVR is kardinal's PromotionStep.
var PromotionStepGVR = schema.GroupVersionResource{Group: "kardinal.io", Version: "v1alpha1", Resource: "promotionsteps"}

// Steps lists the Bundle's PromotionSteps, sorted by environment.
func (e *Env) Steps(ctx context.Context, ns, pipeline, bundle string) ([]v1alpha1.PromotionStep, error) {
	var list v1alpha1.PromotionStepList
	if err := e.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{
		"kardinal.io/pipeline": pipeline,
		"kardinal.io/bundle":   bundle,
	}); err != nil {
		return nil, err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Spec.Environment < list.Items[j].Spec.Environment })
	return list.Items, nil
}

// StepEnvs returns the environments the Bundle has PromotionSteps for.
func StepEnvs(steps []v1alpha1.PromotionStep) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.Spec.Environment)
	}
	return out
}

// WaitBundle waits until check holds for the Bundle and returns it.
func (e *Env) WaitBundle(t *testing.T, ns, name string, timeout time.Duration, what string,
	check func(*v1alpha1.Bundle) (bool, string)) *v1alpha1.Bundle {
	t.Helper()
	var b v1alpha1.Bundle
	Eventually(t, timeout, fmt.Sprintf("bundle %s: %s", name, what), func(ctx context.Context) (bool, string) {
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
			return false, err.Error()
		}
		return check(&b)
	})
	return &b
}

// WaitStep waits until check holds for the Bundle's PromotionStep for env and
// returns it. Unlike WaitStepState it does not stop at a terminal state; check
// decides.
func (e *Env) WaitStep(t *testing.T, ns, pipeline, bundle, env string, timeout time.Duration, what string,
	check func(*v1alpha1.PromotionStep) (bool, string)) *v1alpha1.PromotionStep {
	t.Helper()
	var got *v1alpha1.PromotionStep
	Eventually(t, timeout, fmt.Sprintf("step %s/%s/%s: %s", pipeline, bundle, env, what), func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, ns, pipeline, bundle, env)
		if err != nil {
			return false, err.Error()
		}
		if !ok {
			return false, "no PromotionStep yet"
		}
		got = ps
		return check(ps)
	})
	return got
}

// CondIs reports whether conds has type typ with status and, when reason is
// set, that reason. seen describes the condition for a failure message.
func CondIs(conds []metav1.Condition, typ string, status metav1.ConditionStatus, reason string) (ok bool, seen string) {
	c := meta.FindStatusCondition(conds, typ)
	if c == nil {
		return false, fmt.Sprintf("no %s condition", typ)
	}
	seen = fmt.Sprintf("%s=%s reason=%q message=%q", typ, c.Status, c.Reason, c.Message)
	return c.Status == status && (reason == "" || c.Reason == reason), seen
}

// AuditEvents lists the AuditEvents written for bundle in ns.
func (e *Env) AuditEvents(ctx context.Context, ns, bundle string) ([]v1alpha1.AuditEvent, error) {
	var list v1alpha1.AuditEventList
	if err := e.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/bundle": bundle}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// Events lists the events.k8s.io/v1 Events in ns regarding the object of kind
// and name.
func (e *Env) Events(ctx context.Context, ns, kind, name string) ([]eventsv1.Event, error) {
	list, err := e.Kube.EventsV1().Events(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []eventsv1.Event
	for _, ev := range list.Items {
		if ev.Regarding.Kind == kind && ev.Regarding.Name == name {
			out = append(out, ev)
		}
	}
	return out, nil
}

// Table is what `kubectl get` prints for a resource: the API server's Table
// rendering, keyed by object name.
type Table struct {
	Columns []string
	Rows    map[string][]string
}

// Cell returns the value of column in the row of name.
func (tb Table) Cell(name, column string) string {
	for i, c := range tb.Columns {
		if c == column && i < len(tb.Rows[name]) {
			return tb.Rows[name][i]
		}
	}
	return ""
}

// GetTable fetches the Table kubectl get renders for group/version/resource
// in ns, from the CRD's additionalPrinterColumns.
func (e *Env) GetTable(t *testing.T, group, version, resource, ns string) Table {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := e.Kube.Discovery().RESTClient().Get().
		AbsPath("/apis", group, version, "namespaces", ns, resource).
		SetHeader("Accept", "application/json;as=Table;v=v1;g=meta.k8s.io").
		DoRaw(ctx)
	if err != nil {
		t.Fatalf("get %s table: %v", resource, err)
	}
	var tbl metav1.Table
	if err := json.Unmarshal(raw, &tbl); err != nil {
		t.Fatalf("decode %s table: %v", resource, err)
	}
	out := Table{Rows: map[string][]string{}}
	for _, c := range tbl.ColumnDefinitions {
		out.Columns = append(out.Columns, c.Name)
	}
	for _, r := range tbl.Rows {
		cells := make([]string, 0, len(r.Cells))
		for _, c := range r.Cells {
			cells = append(cells, fmt.Sprint(c))
		}
		if len(cells) > 0 {
			out.Rows[cells[0]] = cells
		}
	}
	return out
}

// RunningPod returns the name of a Running pod in ns matching selector.
func (e *Env) RunningPod(t *testing.T, ns, selector string) string {
	t.Helper()
	var name string
	Eventually(t, 2*time.Minute, "a Running pod "+selector+" in "+ns, func(ctx context.Context) (bool, string) {
		pods, err := e.Kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return false, err.Error()
		}
		for _, p := range pods.Items {
			if p.Status.Phase == "Running" && p.DeletionTimestamp == nil {
				name = p.Name
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d pods, none Running", len(pods.Items))
	})
	return name
}

// PodHTTP sends method path to port of pod through the API server's pod
// proxy and returns the status code and body of whatever the pod answers,
// errors included (client-go's REST client drops the code and body of a
// non-2xx text/plain response). path may carry a query string.
func (e *Env) PodHTTP(ctx context.Context, ns, pod string, port int, method, path string) (int, string, error) {
	hc, err := rest.HTTPClientFor(e.Config)
	if err != nil {
		return 0, "", fmt.Errorf("http client: %w", err)
	}
	u := fmt.Sprintf("%s/api/v1/namespaces/%s/pods/%s:%d/proxy%s", strings.TrimSuffix(e.Config.Host, "/"), ns, pod, port, path)
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return 0, "", fmt.Errorf("request %s: %w", path, err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), err
}

// Manifests reads the YAML documents of file (relative to the test's
// working directory) as unstructured objects, skipping empty documents.
func Manifests(t *testing.T, file string) []*unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	var out []*unstructured.Unstructured
	for {
		var obj map[string]interface{}
		if err := dec.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				return out
			}
			t.Fatalf("decode %s: %v", file, err)
		}
		if len(obj) == 0 {
			continue
		}
		out = append(out, &unstructured.Unstructured{Object: obj})
	}
}

// resourceFor maps obj's kind to its resource through discovery.
func (e *Env) resourceFor(obj *unstructured.Unstructured) (schema.GroupVersionResource, bool, error) {
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(e.Kube.Discovery()))
	gvk := obj.GroupVersionKind()
	m, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, false, fmt.Errorf("map %s: %w", gvk, err)
	}
	return m.Resource, m.Scope.Name() == meta.RESTScopeNameNamespace, nil
}

// DryRunApply is `kubectl apply --server-side --dry-run=server` for obj: the
// API server validates and admits it (schema, CEL rules, defaults) without
// persisting it. It returns the object the server would store.
func (e *Env) DryRunApply(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return e.apply(ctx, obj, []string{metav1.DryRunAll})
}

// Apply server-side applies obj (field manager kardinal-e2e) and returns it.
func (e *Env) Apply(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return e.apply(ctx, obj, nil)
}

func (e *Env) apply(ctx context.Context, obj *unstructured.Unstructured, dryRun []string) (*unstructured.Unstructured, error) {
	gvr, namespaced, err := e.resourceFor(obj)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(obj.Object)
	if err != nil {
		return nil, err
	}
	force := true
	opts := metav1.PatchOptions{FieldManager: "kardinal-e2e", Force: &force, DryRun: dryRun}
	if namespaced {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = "default"
		}
		return e.Dynamic.Resource(gvr).Namespace(ns).Patch(ctx, obj.GetName(), types.ApplyPatchType, raw, opts)
	}
	return e.Dynamic.Resource(gvr).Patch(ctx, obj.GetName(), types.ApplyPatchType, raw, opts)
}

// DeleteOnCleanup deletes obj when the test ends (unless KARDINAL_E2E_KEEP=1)
// and, with wait, waits until it is gone.
func (e *Env) DeleteOnCleanup(t *testing.T, obj *unstructured.Unstructured, wait bool) {
	t.Helper()
	gvr, namespaced, err := e.resourceFor(obj)
	if err != nil {
		t.Fatalf("cleanup of %s: %v", obj.GetName(), err)
	}
	t.Cleanup(func() {
		if os.Getenv(EnvKeep) == "1" {
			return
		}
		ri := e.Dynamic.Resource(gvr)
		var res interface {
			Delete(context.Context, string, metav1.DeleteOptions, ...string) error
			Get(context.Context, string, metav1.GetOptions, ...string) (*unstructured.Unstructured, error)
		} = ri
		if namespaced {
			res = ri.Namespace(obj.GetNamespace())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := res.Delete(ctx, obj.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete %s %s: %v", obj.GetKind(), obj.GetName(), err)
			return
		}
		for wait {
			if _, err := res.Get(ctx, obj.GetName(), metav1.GetOptions{}); apierrors.IsNotFound(err) {
				return
			}
			select {
			case <-ctx.Done():
				t.Errorf("%s %s still exists after delete", obj.GetKind(), obj.GetName())
				return
			case <-time.After(Poll):
			}
		}
	})
}

// ArgoAppSpec creates an Argo CD Application with spec in the argocd
// namespace and deletes it when the test ends. With prune, the Application
// carries Argo CD's resources finalizer, so deleting it also deletes what it
// deployed; cleanup then waits until it is gone.
func (e *Env) ArgoAppSpec(t *testing.T, name string, spec map[string]interface{}, prune bool) {
	t.Helper()
	md := map[string]interface{}{
		"name":      name,
		"namespace": ArgoCDNamespace,
		"labels":    map[string]interface{}{"kardinal.io/e2e": "true"},
	}
	if prune {
		md["finalizers"] = []interface{}{"resources-finalizer.argocd.argoproj.io"}
	}
	app := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": md, "spec": spec,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := e.Dynamic.Resource(ApplicationGVR).Namespace(ArgoCDNamespace).Create(ctx, app, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create Argo CD Application %s: %v", name, err)
	}
	e.DeleteOnCleanup(t, app, prune)
}

// PushTree clones repo's default branch on the test runner, lets change edit
// the checkout in dir, and commits and pushes the result as a developer
// would. Use it for content the git server's API cannot write, such as
// symlinks. It returns the new commit SHA.
func (e *Env) PushTree(t *testing.T, repo gitserver.Repo, message string, change func(dir string)) string {
	t.Helper()
	return e.PushBranch(t, repo, "", message, change)
}

// PushBranch is PushTree on branch (the default branch when empty), for
// example a PR branch someone pushes to by hand.
func (e *Env) PushBranch(t *testing.T, repo gitserver.Repo, branch, message string, change func(dir string)) string {
	t.Helper()
	remote, token, err := gitserver.PushRemote(e.Git, repo)
	if err != nil {
		t.Fatalf("push remote: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	auth := &gogithttp.BasicAuth{Username: "x-access-token", Password: token}
	dir := t.TempDir()
	opts := &gogit.CloneOptions{URL: remote, Auth: auth}
	if branch != "" {
		opts.ReferenceName, opts.SingleBranch = plumbing.NewBranchReferenceName(branch), true
	}
	r, err := gogit.PlainCloneContext(ctx, dir, false, opts)
	if err != nil {
		t.Fatalf("clone %s: %v", repo.Name, err)
	}
	change(dir)
	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		t.Fatalf("git add: %v", err)
	}
	sig := &object.Signature{Name: "e2e-developer", Email: "e2e@example.com", When: time.Now()}
	sha, err := wt.Commit(message, &gogit.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatalf("git commit: %v", err)
	}
	if err := r.PushContext(ctx, &gogit.PushOptions{Auth: auth}); err != nil {
		t.Fatalf("git push %s: %v", repo.Name, err)
	}
	return sha.String()
}

// StateLog records every state each PromotionStep of a namespace passes
// through, from a watch, so a test can assert the order of short-lived states
// a poll could miss.
type StateLog struct {
	mu  sync.Mutex
	seq map[string][]string
}

// States returns the distinct consecutive states recorded for the Bundle's
// step for env.
func (l *StateLog) States(bundle, env string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.seq[bundle+"/"+env]...)
}

func (l *StateLog) record(obj *unstructured.Unstructured) {
	labels := obj.GetLabels()
	state, _, _ := unstructured.NestedString(obj.Object, "status", "state")
	if state == "" {
		state = "(none)"
	}
	key := labels["kardinal.io/bundle"] + "/" + labels["kardinal.io/environment"]
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := l.seq[key]; len(s) == 0 || s[len(s)-1] != state {
		l.seq[key] = append(s, state)
	}
}

// RecordStepStates starts recording the states of the PromotionSteps in ns
// until the test ends.
func (e *Env) RecordStepStates(t *testing.T, ns string) *StateLog {
	t.Helper()
	l := &StateLog{seq: map[string][]string{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	ri := e.Dynamic.Resource(PromotionStepGVR).Namespace(ns)
	list, err := ri.List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list PromotionSteps: %v", err)
	}
	for i := range list.Items {
		l.record(&list.Items[i])
	}
	rv := list.GetResourceVersion()
	// relist resumes from a fresh list after a watch error, recording what
	// changed meanwhile.
	relist := func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
		if l2, err := ri.List(ctx, metav1.ListOptions{}); err == nil {
			for i := range l2.Items {
				l.record(&l2.Items[i])
			}
			rv = l2.GetResourceVersion()
		}
	}
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			w, err := ri.Watch(ctx, metav1.ListOptions{ResourceVersion: rv})
			if err != nil {
				relist()
				continue
			}
			for ev := range w.ResultChan() {
				if ev.Type == watch.Error {
					break
				}
				obj, ok := ev.Object.(*unstructured.Unstructured)
				if !ok {
					continue
				}
				rv = obj.GetResourceVersion()
				if ev.Type == watch.Added || ev.Type == watch.Modified {
					l.record(obj)
				}
			}
			w.Stop()
			relist()
		}
	}()
	return l
}

// JoinStates renders a state sequence for a failure message.
func JoinStates(states []string) string { return strings.Join(states, " -> ") }
