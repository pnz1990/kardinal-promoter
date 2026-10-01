// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// RolloutGVR is Argo Rollouts' Rollout.
var RolloutGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}

// CanaryGVR is Flagger's Canary.
var CanaryGVR = schema.GroupVersionResource{Group: "flagger.app", Version: "v1beta1", Resource: "canaries"}

// Rollout is what a user sees of an Argo Rollouts Rollout.
type Rollout struct {
	// Image is the first container image of the pod template.
	Image string
	// Phase and Message are status.phase and status.message.
	Phase, Message string
	// Aborted is status.abort: Argo Rollouts aborted the update and serves
	// the stable revision.
	Aborted bool
	// Done means Argo Rollouts observed the current spec and the stable
	// ReplicaSet is the current pod template: the rollout finished.
	Done bool
}

func (r Rollout) String() string {
	return fmt.Sprintf("image %s, phase %q (%s), aborted %t, done %t", r.Image, r.Phase, r.Message, r.Aborted, r.Done)
}

// GetRollout reads the Rollout ns/name.
func (e *Env) GetRollout(ctx context.Context, ns, name string) (Rollout, error) {
	u, err := e.Dynamic.Resource(RolloutGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Rollout{}, err
	}
	var r Rollout
	containers, _, _ := unstructured.NestedSlice(u.Object, "spec", "template", "spec", "containers")
	if len(containers) > 0 {
		r.Image, _ = containers[0].(map[string]interface{})["image"].(string)
	}
	r.Phase, _, _ = unstructured.NestedString(u.Object, "status", "phase")
	r.Message, _, _ = unstructured.NestedString(u.Object, "status", "message")
	r.Aborted, _, _ = unstructured.NestedBool(u.Object, "status", "abort")
	observed, _, _ := unstructured.NestedString(u.Object, "status", "observedGeneration")
	stable, _, _ := unstructured.NestedString(u.Object, "status", "stableRS")
	current, _, _ := unstructured.NestedString(u.Object, "status", "currentPodHash")
	r.Done = observed == fmt.Sprint(u.GetGeneration()) && stable != "" && stable == current
	return r, nil
}

// WaitRollout waits until the Rollout ns/name satisfies done and returns it.
func (e *Env) WaitRollout(t *testing.T, ns, name string, timeout time.Duration, what string, done func(Rollout) bool) Rollout {
	t.Helper()
	var got Rollout
	Eventually(t, timeout, fmt.Sprintf("Rollout %s/%s %s", ns, name, what), func(ctx context.Context) (bool, string) {
		r, err := e.GetRollout(ctx, ns, name)
		if err != nil {
			return false, err.Error()
		}
		got = r
		return done(r), r.String()
	})
	return got
}

// WaitRolloutHealthy waits until the Rollout finished rolling out image and
// is Healthy.
func (e *Env) WaitRolloutHealthy(t *testing.T, ns, name, image string, timeout time.Duration) Rollout {
	t.Helper()
	return e.WaitRollout(t, ns, name, timeout, "Healthy on "+image, func(r Rollout) bool {
		return r.Image == image && r.Phase == "Healthy" && r.Done
	})
}

// StableImage is the first container image of the Rollout's stable
// ReplicaSet: the revision Argo Rollouts serves, and goes back to on abort.
func (e *Env) StableImage(t *testing.T, ns, name string) string {
	t.Helper()
	ctx := context.Background()
	u, err := e.Dynamic.Resource(RolloutGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Rollout %s/%s: %v", ns, name, err)
	}
	hash, _, _ := unstructured.NestedString(u.Object, "status", "stableRS")
	if hash == "" {
		t.Fatalf("Rollout %s/%s has no status.stableRS", ns, name)
	}
	rs, err := e.Kube.AppsV1().ReplicaSets(ns).Get(ctx, name+"-"+hash, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get the stable ReplicaSet of Rollout %s/%s: %v", ns, name, err)
	}
	if len(rs.Spec.Template.Spec.Containers) == 0 {
		t.Fatalf("ReplicaSet %s/%s has no containers", ns, rs.Name)
	}
	return rs.Spec.Template.Spec.Containers[0].Image
}

// PauseRollout sets the Rollout's spec.paused, as `kubectl argo rollouts
// pause` and `resume` do.
func (e *Env) PauseRollout(t *testing.T, ns, name string, paused bool) {
	t.Helper()
	patch := fmt.Sprintf(`{"spec":{"paused":%t}}`, paused)
	if _, err := e.Dynamic.Resource(RolloutGVR).Namespace(ns).Patch(context.Background(), name,
		types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		t.Fatalf("set paused=%t on Rollout %s/%s: %v", paused, ns, name, err)
	}
}

// PromoteRollout continues a canary stopped at a pause step, as `kubectl
// argo rollouts promote` does: it clears status.pauseConditions.
func (e *Env) PromoteRollout(t *testing.T, ns, name string) {
	t.Helper()
	if _, err := e.Dynamic.Resource(RolloutGVR).Namespace(ns).Patch(context.Background(), name,
		types.MergePatchType, []byte(`{"status":{"pauseConditions":null}}`), metav1.PatchOptions{}, "status"); err != nil {
		t.Fatalf("promote Rollout %s/%s: %v", ns, name, err)
	}
}

// Canary is what a user sees of a Flagger Canary.
type Canary struct {
	// Phase is status.phase; Message is the message of its Promoted condition,
	// which says why an analysis failed.
	Phase, Message string
	// LastTransition is status.lastTransitionTime.
	LastTransition string
}

func (c Canary) String() string {
	return fmt.Sprintf("phase %q (%s) since %s", c.Phase, c.Message, c.LastTransition)
}

// GetCanary reads the Canary ns/name.
func (e *Env) GetCanary(ctx context.Context, ns, name string) (Canary, error) {
	u, err := e.Dynamic.Resource(CanaryGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Canary{}, err
	}
	var c Canary
	c.Phase, _, _ = unstructured.NestedString(u.Object, "status", "phase")
	c.LastTransition, _, _ = unstructured.NestedString(u.Object, "status", "lastTransitionTime")
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, cond := range conditions {
		m, _ := cond.(map[string]interface{})
		if m["type"] == "Promoted" {
			c.Message, _ = m["message"].(string)
		}
	}
	return c, nil
}

// WaitCanaryPhase waits until the Canary ns/name reaches phase and returns it.
func (e *Env) WaitCanaryPhase(t *testing.T, ns, name, phase string, timeout time.Duration) Canary {
	t.Helper()
	var got Canary
	Eventually(t, timeout, fmt.Sprintf("Canary %s/%s %s", ns, name, phase), func(ctx context.Context) (bool, string) {
		c, err := e.GetCanary(ctx, ns, name)
		if err != nil {
			return false, err.Error()
		}
		got = c
		return c.Phase == phase, c.String()
	})
	return got
}

// SetArgoAutoSync turns automated sync of the Argo CD Application name on
// (prune and self-heal, as ArgoApp creates it) or off. With it off, Argo CD
// still sees new commits but does not apply them: a test uses that to hold
// the cluster on the previous revision, as a slow GitOps sync would.
func (e *Env) SetArgoAutoSync(t *testing.T, name string, on bool) {
	t.Helper()
	patch := `{"spec":{"syncPolicy":{"automated":null}}}`
	if on {
		patch = `{"spec":{"syncPolicy":{"automated":{"prune":true,"selfHeal":true}}}}`
	}
	if _, err := e.Dynamic.Resource(ApplicationGVR).Namespace(ArgoCDNamespace).Patch(context.Background(), name,
		types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		t.Fatalf("set automated sync %t on Argo CD Application %s: %v", on, name, err)
	}
}

// WaitStepMessageAll waits until the Bundle's PromotionStep for env is in state
// with a message containing every one of substrs, and returns it. It fails
// early when the step reaches a terminal state other than state.
func (e *Env) WaitStepMessageAll(t *testing.T, ns, pipeline, bundle, env, state string, timeout time.Duration, substrs ...string) *v1alpha1.PromotionStep {
	t.Helper()
	var got *v1alpha1.PromotionStep
	what := fmt.Sprintf("step %s/%s/%s %s with message %q", pipeline, bundle, env, state, substrs)
	Eventually(t, timeout, what, func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, ns, pipeline, bundle, env)
		if err != nil {
			return false, err.Error()
		}
		if !ok {
			return false, "no PromotionStep yet"
		}
		got = ps
		s := ps.Status.State
		if s != state && isTerminalStep(s) {
			t.Fatalf("step %s reached %s, want %s: %s", ps.Name, s, state, ps.Status.Message)
		}
		seen := fmt.Sprintf("state=%q failures=%d message=%q", s, ps.Status.ConsecutiveHealthFailures, ps.Status.Message)
		if s != state {
			return false, seen
		}
		for _, sub := range substrs {
			if !strings.Contains(ps.Status.Message, sub) {
				return false, seen
			}
		}
		return true, seen
	})
	return got
}

// HoldStep fails the test if the Bundle's PromotionStep for env stops
// satisfying hold at any poll during d. hold returns what it saw.
func (e *Env) HoldStep(t *testing.T, d time.Duration, ns, pipeline, bundle, env, what string,
	hold func(*v1alpha1.PromotionStep) bool) {
	t.Helper()
	Consistently(t, d, what, func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, ns, pipeline, bundle, env)
		if err != nil {
			return false, err.Error()
		}
		if !ok {
			return false, "no PromotionStep"
		}
		return hold(ps), fmt.Sprintf("state=%q failures=%d message=%q",
			ps.Status.State, ps.Status.ConsecutiveHealthFailures, ps.Status.Message)
	})
}
