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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// SetPipelinePaused sets spec.paused with a JSON merge patch, the request
// `kubectl patch pipeline <name> --type merge -p '{"spec":{"paused":true}}'`
// sends. It writes only the Pipeline.
func (e *Env) SetPipelinePaused(t *testing.T, ns, name string, paused bool) {
	t.Helper()
	patch, err := json.Marshal(map[string]interface{}{"spec": map[string]interface{}{"paused": paused}})
	if err != nil {
		t.Fatal(err)
	}
	p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	if err := e.Client.Patch(context.Background(), p, client.RawPatch(types.MergePatchType, patch)); err != nil {
		t.Fatalf("patch Pipeline %s/%s paused=%v: %v", ns, name, paused, err)
	}
}

// WaitPipeline waits until the Pipeline satisfies match and returns it.
func (e *Env) WaitPipeline(t *testing.T, ns, name string, timeout time.Duration, what string,
	match func(*v1alpha1.Pipeline) bool) *v1alpha1.Pipeline {
	t.Helper()
	var p v1alpha1.Pipeline
	Eventually(t, timeout, fmt.Sprintf("Pipeline %s/%s %s", ns, name, what), func(ctx context.Context) (bool, string) {
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &p); err != nil {
			return false, err.Error()
		}
		return match(&p), fmt.Sprintf("paused=%v conditions=%v", p.Spec.Paused, p.Status.Conditions)
	})
	return &p
}

// WaitFreezeGate waits until the Pipeline's freeze PolicyGate (freeze-<name>)
// exists and returns it.
func (e *Env) WaitFreezeGate(t *testing.T, ns, pipeline string, timeout time.Duration) *v1alpha1.PolicyGate {
	t.Helper()
	var g v1alpha1.PolicyGate
	Eventually(t, timeout, fmt.Sprintf("freeze gate of %s/%s", ns, pipeline), func(ctx context.Context) (bool, string) {
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: "freeze-" + pipeline}, &g); err != nil {
			return false, err.Error()
		}
		return true, ""
	})
	return &g
}

// WaitNoFreezeGate waits until the Pipeline's freeze PolicyGate is gone.
func (e *Env) WaitNoFreezeGate(t *testing.T, ns, pipeline string, timeout time.Duration) {
	t.Helper()
	Eventually(t, timeout, fmt.Sprintf("no freeze gate of %s/%s", ns, pipeline), func(ctx context.Context) (bool, string) {
		var g v1alpha1.PolicyGate
		err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: "freeze-" + pipeline}, &g)
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, "freeze gate " + g.Name + " exists"
	})
}

// StepHeldIn fails the test unless the Bundle's step for env stays in state
// with a message containing msg for all of d. StepHeld is StepHeldIn Pending.
func (e *Env) StepHeldIn(t *testing.T, ns, pipeline, bundle, env, state, msg string, d time.Duration) *v1alpha1.PromotionStep {
	t.Helper()
	var last *v1alpha1.PromotionStep
	Consistently(t, d, fmt.Sprintf("step %s/%s held in %s with %q", bundle, env, state, msg), func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, ns, pipeline, bundle, env)
		if err != nil || !ok {
			return false, fmt.Sprintf("step lookup: ok=%v err=%v", ok, err)
		}
		last = ps
		return stepState(ps) == state && strings.Contains(ps.Status.Message, msg),
			fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	return last
}
