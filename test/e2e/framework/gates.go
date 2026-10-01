// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// PolicyNamespace is the controller's default org policy namespace
// (--policy-namespaces).
const PolicyNamespace = "platform-policies"

// Gate is a PolicyGate template in ns that applies to env. recheck is its
// spec.recheckInterval ("" leaves the CRD default).
func Gate(ns, name, env, expression, recheck string) *v1alpha1.PolicyGate {
	return &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{
			"kardinal.io/scope":      "team",
			"kardinal.io/applies-to": env,
			"kardinal.io/type":       "gate",
		}},
		Spec: v1alpha1.PolicyGateSpec{Expression: expression, RecheckInterval: recheck},
	}
}

// CreateGate creates the PolicyGate template g. Gates outside a test
// namespace (org gates in PolicyNamespace) are deleted when the test ends.
func (e *Env) CreateGate(t *testing.T, g *v1alpha1.PolicyGate) {
	t.Helper()
	if err := e.Client.Create(context.Background(), g); err != nil {
		t.Fatalf("create PolicyGate %s/%s: %v", g.Namespace, g.Name, err)
	}
	if !strings.HasPrefix(g.Namespace, "e2e-") {
		t.Cleanup(func() {
			if err := e.Client.Delete(context.Background(), g); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("delete PolicyGate %s/%s: %v", g.Namespace, g.Name, err)
			}
		})
	}
}

// GateInstances lists the gate instances the Graph created in ns for bundle's
// env from the template named template (label kardinal.io/gate-template).
func (e *Env) GateInstances(ctx context.Context, ns, bundle, env, template string) ([]v1alpha1.PolicyGate, error) {
	var list v1alpha1.PolicyGateList
	if err := e.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{
		"kardinal.io/bundle":        bundle,
		"kardinal.io/environment":   env,
		"kardinal.io/gate-template": template,
	}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// GateInstance is the one instance of template for bundle's env. ok is false
// when the Graph has not created it yet.
func (e *Env) GateInstance(ctx context.Context, ns, bundle, env, template string) (*v1alpha1.PolicyGate, bool, error) {
	items, err := e.GateInstances(ctx, ns, bundle, env, template)
	switch {
	case err != nil:
		return nil, false, err
	case len(items) == 0:
		return nil, false, nil
	case len(items) > 1:
		return nil, false, fmt.Errorf("%d instances of gate %s for %s/%s", len(items), template, bundle, env)
	}
	return &items[0], true, nil
}

// WaitGate waits until the instance of template for bundle's env satisfies
// match and returns it.
func (e *Env) WaitGate(t *testing.T, ns, bundle, env, template string, timeout time.Duration,
	what string, match func(*v1alpha1.PolicyGate) bool) *v1alpha1.PolicyGate {
	t.Helper()
	var got *v1alpha1.PolicyGate
	Eventually(t, timeout, fmt.Sprintf("gate %s on %s/%s %s", template, bundle, env, what), func(ctx context.Context) (bool, string) {
		g, ok, err := e.GateInstance(ctx, ns, bundle, env, template)
		if err != nil {
			return false, err.Error()
		}
		if !ok {
			return false, "no gate instance yet"
		}
		got = g
		return match(g), DescribeGate(g)
	})
	return got
}

// WaitGateReady waits until the instance's status.ready is ready and its
// reason contains reason.
func (e *Env) WaitGateReady(t *testing.T, ns, bundle, env, template string, ready bool, reason string,
	timeout time.Duration) *v1alpha1.PolicyGate {
	t.Helper()
	return e.WaitGate(t, ns, bundle, env, template, timeout,
		fmt.Sprintf("ready=%v with reason containing %q", ready, reason), func(g *v1alpha1.PolicyGate) bool {
			return g.Status.LastEvaluatedAt != nil && g.Status.Ready == ready && strings.Contains(g.Status.Reason, reason)
		})
}

// DescribeGate is a one-line summary of a gate's status for wait messages.
func DescribeGate(g *v1alpha1.PolicyGate) string {
	at := "never"
	if g.Status.LastEvaluatedAt != nil {
		at = g.Status.LastEvaluatedAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%s ready=%v evaluated=%s reason=%q", g.Name, g.Status.Ready, at, g.Status.Reason)
}

// SetBundleLabel sets the Bundle label key to value, or removes it when value
// is empty. Gates read labels as bundle.labels, so this opens or closes a gate
// whose expression tests one.
func (e *Env) SetBundleLabel(t *testing.T, ns, bundle, key, value string) {
	t.Helper()
	var v interface{}
	if value != "" {
		v = value
	}
	patch, err := json.Marshal(map[string]interface{}{"metadata": map[string]interface{}{"labels": map[string]interface{}{key: v}}})
	if err != nil {
		t.Fatal(err)
	}
	b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: bundle}}
	if err := e.Client.Patch(context.Background(), b, client.RawPatch(types.MergePatchType, patch)); err != nil {
		t.Fatalf("label Bundle %s/%s %s=%q: %v", ns, bundle, key, value, err)
	}
}

// CreateChangeWindow creates the cluster-scoped window cw and deletes it when
// the test ends. Name it after the test's namespace so runs never collide.
func (e *Env) CreateChangeWindow(t *testing.T, cw *v1alpha1.ChangeWindow) {
	t.Helper()
	if err := e.Client.Create(context.Background(), cw); err != nil {
		t.Fatalf("create ChangeWindow %s: %v", cw.Name, err)
	}
	t.Cleanup(func() {
		if err := e.Client.Delete(context.Background(), cw); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete ChangeWindow %s: %v", cw.Name, err)
		}
	})
}

// WaitChangeWindow waits until the window satisfies match and returns it.
func (e *Env) WaitChangeWindow(t *testing.T, name string, timeout time.Duration, what string,
	match func(*v1alpha1.ChangeWindow) bool) *v1alpha1.ChangeWindow {
	t.Helper()
	var cw v1alpha1.ChangeWindow
	Eventually(t, timeout, "ChangeWindow "+name+" "+what, func(ctx context.Context) (bool, string) {
		if err := e.Client.Get(ctx, types.NamespacedName{Name: name}, &cw); err != nil {
			return false, err.Error()
		}
		return match(&cw), fmt.Sprintf("active=%v reason=%q conditions=%v", cw.Status.Active, cw.Status.Reason, cw.Status.Conditions)
	})
	return &cw
}

// NoStep fails the test if the Bundle gets a PromotionStep for env during d:
// the Graph holds an environment whose gates are not ready by not creating
// its step.
func (e *Env) NoStep(t *testing.T, ns, pipeline, bundle, env string, d time.Duration) {
	t.Helper()
	Consistently(t, d, fmt.Sprintf("no %s step for %s", env, bundle), func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, ns, pipeline, bundle, env)
		if err != nil {
			return false, err.Error()
		}
		if ok {
			return false, fmt.Sprintf("step %s exists: state=%q message=%q", ps.Name, ps.Status.State, ps.Status.Message)
		}
		return true, ""
	})
}

// WaitStepMessage waits until the Bundle's step for env is in state with a
// message containing msg.
func (e *Env) WaitStepMessage(t *testing.T, ns, pipeline, bundle, env, state, msg string, timeout time.Duration) *v1alpha1.PromotionStep {
	t.Helper()
	var got *v1alpha1.PromotionStep
	Eventually(t, timeout, fmt.Sprintf("step %s/%s %s with message %q", bundle, env, state, msg), func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, ns, pipeline, bundle, env)
		if err != nil {
			return false, err.Error()
		}
		if !ok {
			return false, "no PromotionStep yet"
		}
		got = ps
		return stepState(ps) == state && strings.Contains(ps.Status.Message, msg),
			fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	return got
}

// StepHeld fails the test unless the Bundle's step for env stays Pending with
// a message containing msg for all of d.
func (e *Env) StepHeld(t *testing.T, ns, pipeline, bundle, env, msg string, d time.Duration) {
	t.Helper()
	Consistently(t, d, fmt.Sprintf("step %s/%s held with %q", bundle, env, msg), func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, ns, pipeline, bundle, env)
		if err != nil || !ok {
			return false, fmt.Sprintf("step lookup: ok=%v err=%v", ok, err)
		}
		return stepState(ps) == "Pending" && strings.Contains(ps.Status.Message, msg),
			fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
}

// stepState is the step's state, Pending when the controller has not set one.
func stepState(ps *v1alpha1.PromotionStep) string {
	if ps.Status.State == "" {
		return "Pending"
	}
	return ps.Status.State
}

// CreateBundleObject creates b through the API, as the Bundle API and CI
// integrations do, for fields the CLI has no flag for (labels, intent). An
// empty name becomes generateName "<pipeline>-". It returns the Bundle's name.
func (e *Env) CreateBundleObject(t *testing.T, b *v1alpha1.Bundle) string {
	t.Helper()
	if b.Name == "" && b.GenerateName == "" {
		b.GenerateName = b.Spec.Pipeline + "-"
	}
	lifecycle.StampCreatedAt(b, time.Now())
	if err := e.Client.Create(context.Background(), b); err != nil {
		t.Fatalf("create Bundle in %s: %v", b.Namespace, err)
	}
	return b.Name
}

// Override adds o to the gate instance's spec.overrides, as kardinal
// override does.
func (e *Env) Override(t *testing.T, g *v1alpha1.PolicyGate, o v1alpha1.PolicyGateOverride) {
	t.Helper()
	ctx := context.Background()
	var cur v1alpha1.PolicyGate
	if err := e.Client.Get(ctx, client.ObjectKeyFromObject(g), &cur); err != nil {
		t.Fatalf("get PolicyGate %s: %v", g.Name, err)
	}
	patch, err := json.Marshal(map[string]interface{}{"spec": map[string]interface{}{
		"overrides": append(cur.Spec.Overrides, o)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Client.Patch(ctx, &cur, client.RawPatch(types.MergePatchType, patch)); err != nil {
		t.Fatalf("override PolicyGate %s: %v", g.Name, err)
	}
}

// ExplainGate runs kardinal explain for env and returns the STATE of the gate
// named name (the template name, as explain shows it) and its whole row. ok
// is false when explain lists no such gate.
func (e *Env) ExplainGate(t *testing.T, ns, pipeline, env, name string) (state, row string, ok bool) {
	t.Helper()
	out := e.MustKardinal(t, ns, "explain", pipeline, "--env", env)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 5 && f[0] == env && f[2] == "PolicyGate" && f[3] == name {
			return f[4], line, true
		}
	}
	return "", out, false
}

// WaitExplainGate waits until kardinal explain shows the gate named name on
// env in state, and returns its row.
func (e *Env) WaitExplainGate(t *testing.T, ns, pipeline, env, name, state string, timeout time.Duration) string {
	t.Helper()
	var row string
	Eventually(t, timeout, fmt.Sprintf("explain shows gate %s on %s as %s", name, env, state), func(context.Context) (bool, string) {
		got, r, ok := e.ExplainGate(t, ns, pipeline, env, name)
		row = r
		return ok && got == state, r
	})
	return row
}

// EnsureNamespace creates the namespace name if it does not exist and never
// deletes it: shared namespaces such as PolicyNamespace outlive every test.
// Tests put only their own uniquely named objects in it.
func (e *Env) EnsureNamespace(t *testing.T, name string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := e.Client.Create(context.Background(), ns); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %s: %v", name, err)
	}
}

// GateInstanceName is the name the Graph gives the instance of the template
// templateNS/template for bundle's env ("<gate>-<namespace>-<env>--<bundle>",
// pkg/graph gateNodeK8sName). It holds for lowercase names that are not
// hash-shortened, which every test name is. Use it when gates of the same name
// come from several namespaces, so the template label alone is ambiguous.
func GateInstanceName(templateNS, template, env, bundle string) string {
	return fmt.Sprintf("%s-%s-%s--%s", template, templateNS, env, bundle)
}

// WaitGateNamed waits until the gate instance ns/name exists and satisfies
// match, and returns it.
func (e *Env) WaitGateNamed(t *testing.T, ns, name string, timeout time.Duration, what string,
	match func(*v1alpha1.PolicyGate) bool) *v1alpha1.PolicyGate {
	t.Helper()
	var g v1alpha1.PolicyGate
	Eventually(t, timeout, fmt.Sprintf("gate %s %s", name, what), func(ctx context.Context) (bool, string) {
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &g); err != nil {
			return false, err.Error()
		}
		return match(&g), DescribeGate(&g)
	})
	return &g
}

// Evaluated matches a gate evaluated to ready with a reason containing reason.
func Evaluated(ready bool, reason string) func(*v1alpha1.PolicyGate) bool {
	return func(g *v1alpha1.PolicyGate) bool {
		return g.Status.LastEvaluatedAt != nil && g.Status.Ready == ready && strings.Contains(g.Status.Reason, reason)
	}
}
