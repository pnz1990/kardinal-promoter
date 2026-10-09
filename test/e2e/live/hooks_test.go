//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// hookTimeout bounds a test's wait for a HookRun: an image pull on a cold
// node plus the hook's own run.
const hookTimeout = 3 * time.Minute

// hookJob is a JobSpec that runs script with sh in the podinfo image (on
// every node already, and it has busybox sh and wget). sa, when set, is the
// Pod's ServiceAccount.
func hookJob(t *testing.T, script, sa string) runtime.RawExtension {
	t.Helper()
	return hookJobWith(t, script, sa, nil, nil)
}

// hookJobWith is hookJob with extra JobSpec and container fields.
func hookJobWith(t *testing.T, script, sa string, job, container map[string]interface{}) runtime.RawExtension {
	t.Helper()
	c := map[string]interface{}{
		"name": "hook", "image": fixtures.Image + ":" + fixtures.V1,
		"command": []interface{}{"sh", "-c", script},
	}
	for k, v := range container {
		c[k] = v
	}
	pod := map[string]interface{}{"containers": []interface{}{c}}
	if sa != "" {
		pod["serviceAccountName"] = sa
	}
	spec := map[string]interface{}{"template": map[string]interface{}{"spec": pod}}
	for k, v := range job {
		spec[k] = v
	}
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	return runtime.RawExtension{Raw: raw}
}

// hookRun reads a HookRun; ok is false while it does not exist.
func hookRun(ctx context.Context, e *framework.Env, ns, name string) (*v1alpha1.HookRun, bool, error) {
	var hr v1alpha1.HookRun
	err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &hr)
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &hr, true, nil
}

// waitHookRun waits until the HookRun reaches phase; it fails at once on the
// other terminal phase.
func waitHookRun(t *testing.T, e *framework.Env, ns, name, phase string) *v1alpha1.HookRun {
	t.Helper()
	var got *v1alpha1.HookRun
	framework.Eventually(t, hookTimeout, fmt.Sprintf("HookRun %s to be %s", name, phase), func(ctx context.Context) (bool, string) {
		hr, ok, err := hookRun(ctx, e, ns, name)
		if err != nil {
			return false, err.Error()
		}
		if !ok {
			return false, "no HookRun yet"
		}
		got = hr
		p := hr.Status.Phase
		if p != phase && (p == v1alpha1.HookRunSucceeded || p == v1alpha1.HookRunFailed) {
			t.Fatalf("HookRun %s is %s, want %s: %s", name, p, phase, hr.Status.Message)
		}
		return p == phase, fmt.Sprintf("phase=%q message=%q", p, hr.Status.Message)
	})
	return got
}

// hookPods lists the Pods of a HookRun's Job.
func hookPods(ctx context.Context, e *framework.Env, ns, hookRun string) ([]corev1.Pod, error) {
	pods, err := e.Kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "kardinal.io/hookrun=" + hookRun})
	if err != nil {
		return nil, err
	}
	return pods.Items, nil
}

// hookLog returns the log of the first Pod of a HookRun's Job.
func hookLog(t *testing.T, e *framework.Env, ns, hookRun string) string {
	t.Helper()
	ctx := context.Background()
	pods, err := hookPods(ctx, e, ns, hookRun)
	require.NoError(t, err)
	require.NotEmpty(t, pods, "HookRun %s has no Pod", hookRun)
	raw, err := e.Kube.CoreV1().Pods(ns).GetLogs(pods[0].Name, &corev1.PodLogOptions{}).DoRaw(ctx)
	require.NoError(t, err)
	return string(bytes.TrimSpace(raw))
}

// TestStep_HooksPreAndPost runs two pre-deploy hooks and one post-deploy
// hook on prod. The pre hooks run one after another, before the promotion
// starts: while the first runs, prod's step waits in Pending and prod still
// runs and has v1 in git. A "${HOSTNAME}" in a hook's command reaches the
// shell unchanged (kro does not read it as an expression). After the health
// check passed the step is Verifying and the post hook, a real integration
// test, fetches the new version from prod's Service; prod is Verified only
// after it succeeded. A hook Job deleted after it succeeded is not run
// again, and deleting the Bundle deletes its HookRuns, their Jobs and Pods.
//
// Covers HOOK-PRE-01, HOOK-ORDER-01, HOOK-POST-01, HOOK-LITERAL-01, HOOK-RERUN-01, HOOK-GC-01.
func TestStep_HooksPreAndPost(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	states := e.RecordStepStates(t, a.ns)
	p := a.pipeline(nil)
	smoke := fmt.Sprintf(`for i in 1 2 3 4 5 6 7 8 9 10; do wget -qO- http://%s:9898/version | grep -q '%s' && exit 0; sleep 3; done; exit 1`,
		fixtures.Workload("prod"), fixtures.V2)
	p.Spec.Environments[1].Hooks = []v1alpha1.HookSpec{
		{Name: "migrate", Phase: "pre", Job: hookJob(t, `echo "migrate on ${HOSTNAME}"; sleep 20`, "")},
		{Name: "seed", Phase: "pre", Job: hookJob(t, `echo seed`, "")},
		{Name: "smoke", Phase: "post", Job: hookJob(t, smoke, "")},
	}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	migrate := graph.HookRunName(pipelineName, bundle, "prod", "pre", "migrate")
	seed := graph.HookRunName(pipelineName, bundle, "prod", "pre", "seed")
	post := graph.HookRunName(pipelineName, bundle, "prod", "post", "smoke")

	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	waitHookRun(t, e, a.ns, migrate, v1alpha1.HookRunRunning)
	// The migration runs before the deploy: the step waits, prod is unchanged.
	framework.Eventually(t, time.Minute, "prod step waiting for the pre hook", func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil || !ok {
			return false, fmt.Sprintf("step: %v %v", ok, err)
		}
		return ps.Status.State == "" && strings.Contains(ps.Status.Message, "waiting for pre-deploy hook migrate"),
			fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	_, seedExists, err := hookRun(ctx, e, a.ns, seed)
	require.NoError(t, err)
	assert.False(t, seedExists, "the second pre hook waits for the first")
	a.fileHas(t, "prod", fixtures.V1, "prod in git while the pre hook runs")
	a.running(t, "prod", fixtures.Image+":"+fixtures.V1, "prod while the pre hook runs")

	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout+2*hookTimeout)
	a.running(t, "prod", imageV2, "prod after the promotion")
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)

	m := waitHookRun(t, e, a.ns, migrate, v1alpha1.HookRunSucceeded)
	s := waitHookRun(t, e, a.ns, seed, v1alpha1.HookRunSucceeded)
	sm := waitHookRun(t, e, a.ns, post, v1alpha1.HookRunSucceeded)
	require.NotNil(t, m.Status.FinishedAt)
	require.NotNil(t, s.Status.StartedAt)
	assert.False(t, s.Status.StartedAt.Before(m.Status.FinishedAt), "seed started (%s) after migrate finished (%s)",
		s.Status.StartedAt, m.Status.FinishedAt)
	require.NotNil(t, ps.Status.VerificationStartedAt)
	require.NotNil(t, sm.Status.StartedAt)
	assert.False(t, sm.Status.StartedAt.Before(ps.Status.VerificationStartedAt), "the post hook started after the health check passed")
	c := meta.FindStatusCondition(ps.Status.Conditions, "Verified")
	require.NotNil(t, c)
	assert.Equal(t, "PostHooksSucceeded", c.Reason)
	checkStates(t, "prod", states.States(bundle, "prod"), "Promoting", "HealthChecking", "Verifying", "Verified")
	checkStates(t, "test", states.States(bundle, "test"), "Promoting", "HealthChecking", "Verified")

	logs := hookLog(t, e, a.ns, migrate)
	assert.True(t, strings.HasPrefix(logs, "migrate on "), "migrate log %q", logs)
	assert.NotContains(t, logs, "${HOSTNAME}", "the shell expanded the variable")
	assert.NotEqual(t, "migrate on", logs, "HOSTNAME expanded to the Pod name")

	// A Job deleted after it succeeded is not run again.
	job, err := e.Kube.BatchV1().Jobs(a.ns).Get(ctx, migrate, metav1.GetOptions{})
	require.NoError(t, err)
	bg := metav1.DeletePropagationBackground
	require.NoError(t, e.Kube.BatchV1().Jobs(a.ns).Delete(ctx, migrate, metav1.DeleteOptions{PropagationPolicy: &bg}))
	framework.Consistently(t, 20*time.Second, "a finished hook stays finished and is not re-run", func(ctx context.Context) (bool, string) {
		hr, ok, err := hookRun(ctx, e, a.ns, migrate)
		if err != nil || !ok {
			return false, fmt.Sprintf("HookRun: %v %v", ok, err)
		}
		j, err := e.Kube.BatchV1().Jobs(a.ns).Get(ctx, migrate, metav1.GetOptions{})
		if err == nil && j.UID != job.UID {
			return false, "a new Job " + string(j.UID) + " was created"
		}
		return hr.Status.Phase == v1alpha1.HookRunSucceeded, "phase=" + hr.Status.Phase
	})

	// Deleting the Bundle deletes its Graph, HookRuns, Jobs and Pods.
	var b v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: bundle}, &b))
	require.NoError(t, e.Client.Delete(ctx, &b))
	framework.Eventually(t, 2*time.Minute, "the Bundle's hooks to be gone", func(ctx context.Context) (bool, string) {
		var hrs v1alpha1.HookRunList
		if err := e.Client.List(ctx, &hrs, client.InNamespace(a.ns), client.MatchingLabels{"kardinal.io/bundle": bundle}); err != nil {
			return false, err.Error()
		}
		jobs, err := e.Kube.BatchV1().Jobs(a.ns).List(ctx, metav1.ListOptions{LabelSelector: "kardinal.io/bundle=" + bundle})
		if err != nil {
			return false, err.Error()
		}
		pods, err := e.Kube.CoreV1().Pods(a.ns).List(ctx, metav1.ListOptions{LabelSelector: "kardinal.io/hookrun"})
		if err != nil {
			return false, err.Error()
		}
		return len(hrs.Items)+len(jobs.Items)+len(pods.Items) == 0,
			fmt.Sprintf("%d HookRuns, %d Jobs, %d Pods left", len(hrs.Items), len(jobs.Items), len(pods.Items))
	})
}

// TestStep_PreHookFailureStopsPromotion: a failed pre-deploy hook fails the
// step before it changes anything: no commit, no deploy, the Bundle Failed.
//
// Covers HOOK-PRE-FAIL-01.
func TestStep_PreHookFailureStopsPromotion(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	states := e.RecordStepStates(t, a.ns)
	p := a.pipeline(nil)
	p.Spec.Environments[0].Hooks = []v1alpha1.HookSpec{
		{Name: "migrate", Phase: "pre", Job: hookJob(t, `echo "schema check failed"; exit 3`, "")},
	}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	hr := waitHookRun(t, e, a.ns, graph.HookRunName(pipelineName, bundle, "prod", "pre", "migrate"), v1alpha1.HookRunFailed)
	assert.Contains(t, hr.Status.Message, "failed")

	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", 2*time.Minute)
	assert.Contains(t, ps.Status.Message, "pre-deploy hook migrate (HookRun ")
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
	checkStates(t, "prod", states.States(bundle, "prod"), "Failed")
	a.fileHas(t, "prod", fixtures.V1, "prod in git after the failed pre hook")
	a.running(t, "prod", fixtures.Image+":"+fixtures.V1, "prod after the failed pre hook")
}

// TestStep_PostHookFailureFailsEnvironment: a failed post-deploy hook fails
// the environment (onHealthFailure none) after its change landed, and the
// next environment is never promoted.
//
// Covers HOOK-POST-FAIL-01.
func TestStep_PostHookFailureFailsEnvironment(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	p := a.pipeline(nil)
	p.Spec.Environments[0].Hooks = []v1alpha1.HookSpec{
		{Name: "e2e", Phase: "post", Job: hookJob(t, `echo "3 of 40 integration tests failed"; exit 1`, "")},
	}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verifying", promoteTimeout)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", hookTimeout)
	assert.Contains(t, ps.Status.Message, "post-deploy hook e2e (HookRun ")
	a.running(t, "test", imageV2, "the change landed before the post hook ran")
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
	framework.Consistently(t, 20*time.Second, "prod is not promoted", func(ctx context.Context) (bool, string) {
		_, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil {
			return false, err.Error()
		}
		return !ok, "prod has a PromotionStep"
	})
	_ = ctx
}

// TestStep_HookServiceAccountRefused: a hook whose Pod names a
// ServiceAccount outside --hook-service-accounts (here the Graph's own,
// which is never allowed) fails without a Job, and so does the step.
//
// Covers HOOK-SA-01.
func TestStep_HookServiceAccountRefused(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	p := a.pipeline(nil)
	p.Spec.Environments[0].Hooks = []v1alpha1.HookSpec{
		{Name: "migrate", Phase: "pre", Job: hookJob(t, `echo hi`, graph.DefaultGraphServiceAccount)},
	}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	name := graph.HookRunName(pipelineName, bundle, "prod", "pre", "migrate")
	hr := waitHookRun(t, e, a.ns, name, v1alpha1.HookRunFailed)
	assert.Contains(t, hr.Status.Message, "Graph ServiceAccount")
	_, err := e.Kube.BatchV1().Jobs(a.ns).Get(ctx, name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "no Job was created: %v", err)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", 2*time.Minute)
	assert.Contains(t, ps.Status.Message, "pre-deploy hook migrate (HookRun ")
	a.fileHas(t, "prod", fixtures.V1, "prod in git")
}

// TestStep_HookEditAndSupersede: a Pipeline edit while a pre hook runs
// reaches the HookRun spec (the Graph is re-translated in place) but not the
// running Job: the HookRun reports SpecChangedAfterStart and the Graph keeps
// working. A newer Bundle supersedes the old one while its hook still runs:
// the old step never starts, the running hook finishes, and the new Bundle
// runs the edited hook and is promoted.
//
// Covers HOOK-EDIT-01, HOOK-SUPERSEDE-01.
func TestStep_HookEditAndSupersede(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	states := e.RecordStepStates(t, a.ns)
	p := a.pipeline(nil)
	p.Spec.Environments[0].Hooks = []v1alpha1.HookSpec{
		{Name: "migrate", Phase: "pre", Job: hookJob(t, `echo original; sleep 40`, "")},
	}
	a.apply(t, p)
	first := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	firstHook := graph.HookRunName(pipelineName, first, "prod", "pre", "migrate")
	waitHookRun(t, e, a.ns, firstHook, v1alpha1.HookRunRunning)

	// Edit the hook while it runs.
	var live v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &live))
	live.Spec.Environments[0].Hooks[0].Job = hookJob(t, `echo edited`, "")
	require.NoError(t, e.Client.Update(ctx, &live))
	framework.Eventually(t, time.Minute, "the running HookRun reports the edit", func(ctx context.Context) (bool, string) {
		hr, ok, err := hookRun(ctx, e, a.ns, firstHook)
		if err != nil || !ok {
			return false, fmt.Sprintf("%v %v", ok, err)
		}
		return meta.IsStatusConditionTrue(hr.Status.Conditions, "SpecChangedAfterStart"),
			fmt.Sprintf("phase=%s conditions=%v", hr.Status.Phase, hr.Status.Conditions)
	})
	var b v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: first}, &b))
	accepted := meta.FindStatusCondition(b.Status.Conditions, "GraphAccepted")
	assert.True(t, accepted == nil || accepted.Status == metav1.ConditionTrue, "the edit did not break the Graph: %v", accepted)

	second := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitBundlePhase(t, a.ns, first, "Superseded", time.Minute)
	waitHookRun(t, e, a.ns, firstHook, v1alpha1.HookRunSucceeded)
	assert.Equal(t, "original", hookLog(t, e, a.ns, firstHook), "the running Job kept its spec")

	secondHook := graph.HookRunName(pipelineName, second, "prod", "pre", "migrate")
	waitHookRun(t, e, a.ns, secondHook, v1alpha1.HookRunSucceeded)
	assert.Equal(t, "edited", hookLog(t, e, a.ns, secondHook))
	e.WaitStepState(t, a.ns, pipelineName, second, "prod", "Verified", promoteTimeout)
	a.running(t, "prod", fixtures.Image+":"+fixtures.V3, "prod runs the newer Bundle")
	for _, st := range states.States(first, "prod") {
		assert.NotEqual(t, "Promoting", st, "the superseded Bundle's step never started: %s", framework.JoinStates(states.States(first, "prod")))
	}
}

// TestStep_HookTTLZeroStillSucceeds: a hook whose Job sets
// ttlSecondsAfterFinished: 0 still records Succeeded (the TTL is dropped;
// regression, QA #1493: the Job vanished as it finished and read as
// deleted before it finished).
//
// Covers HOOK-TTL-01.
func TestStep_HookTTLZeroStillSucceeds(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	p := a.pipeline(nil)
	p.Spec.Environments[0].Hooks = []v1alpha1.HookSpec{
		{Name: "migrate", Phase: "pre", Job: hookJobWith(t, `echo ok`, "", map[string]interface{}{"ttlSecondsAfterFinished": 0}, nil)},
	}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	name := graph.HookRunName(pipelineName, bundle, "prod", "pre", "migrate")
	waitHookRun(t, e, a.ns, name, v1alpha1.HookRunSucceeded)
	job, err := e.Kube.BatchV1().Jobs(a.ns).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err, "the Job is kept: its TTL was dropped")
	assert.Nil(t, job.Spec.TTLSecondsAfterFinished)
	require.NotNil(t, job.Spec.BackoffLimit)
	assert.Equal(t, int32(0), *job.Spec.BackoffLimit)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
}

// TestStep_HookRenamedMidFlight: renaming a pre hook while it runs does not
// cut the running Job off (the old HookRun is held by its finalizer until
// the Job ends) and does not overlap it (the new HookRun waits for it); the
// step then waits for the renamed hook and promotes.
//
// Covers HOOK-RENAME-01.
func TestStep_HookRenamedMidFlight(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	p := a.pipeline(nil)
	p.Spec.Environments[0].Hooks = []v1alpha1.HookSpec{
		{Name: "migrate", Phase: "pre", Job: hookJob(t, `sleep 30; echo old-done`, "")},
	}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	oldName := graph.HookRunName(pipelineName, bundle, "prod", "pre", "migrate")
	waitHookRun(t, e, a.ns, oldName, v1alpha1.HookRunRunning)

	var live v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &live))
	live.Spec.Environments[0].Hooks[0].Name = "migrate-v2"
	live.Spec.Environments[0].Hooks[0].Job = hookJob(t, `echo new-done`, "")
	require.NoError(t, e.Client.Update(ctx, &live))
	newName := graph.HookRunName(pipelineName, bundle, "prod", "pre", "migrate-v2")

	newRun := waitHookRun(t, e, a.ns, newName, v1alpha1.HookRunSucceeded)
	// The old Job ran to its end: its Pod succeeded and printed its output.
	old, err := e.Kube.BatchV1().Jobs(a.ns).Get(ctx, oldName, metav1.GetOptions{})
	if err == nil {
		assert.Equal(t, int32(1), old.Status.Succeeded, "the old Job completed")
		require.NotNil(t, old.Status.CompletionTime)
		// The new HookRun records its start at once and then waits for the
		// old one: its Job is what must come after the old Job finished.
		newJob, err := e.Kube.BatchV1().Jobs(a.ns).Get(ctx, newRun.Status.JobName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.False(t, newJob.CreationTimestamp.Before(old.Status.CompletionTime),
			"the new hook's Job was created (%s) after the old one finished (%s)", newJob.CreationTimestamp, old.Status.CompletionTime)
	} else {
		// The old HookRun was released once its Job ended; its Job went with it.
		require.True(t, apierrors.IsNotFound(err), "get old Job: %v", err)
	}
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	framework.Eventually(t, time.Minute, "the old HookRun to be gone", func(ctx context.Context) (bool, string) {
		_, ok, err := hookRun(ctx, e, a.ns, oldName)
		if err != nil {
			return false, err.Error()
		}
		return !ok, "still there"
	})
}

// TestStep_HookAddedAfterStart: a pre hook added to the Pipeline after the
// step started is Skipped (not run after the deploy), and the step records
// condition HooksSkipped; the step still finishes.
//
// Covers HOOK-LATE-01.
func TestStep_HookAddedAfterStart(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)

	var live v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &live))
	live.Spec.Environments[0].Hooks = []v1alpha1.HookSpec{{Name: "late", Phase: "pre", Job: hookJob(t, `echo late`, "")}}
	require.NoError(t, e.Client.Update(ctx, &live))
	name := graph.HookRunName(pipelineName, bundle, "prod", "pre", "late")
	hr := waitHookRun(t, e, a.ns, name, v1alpha1.HookRunSkipped)
	assert.Contains(t, hr.Status.Message, "after the step had already started")
	_, err := e.Kube.BatchV1().Jobs(a.ns).Get(ctx, name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "no Job for a skipped hook: %v", err)
	framework.Eventually(t, time.Minute, "the step records the skip", func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil || !ok {
			return false, fmt.Sprintf("%v %v", ok, err)
		}
		c := meta.FindStatusCondition(ps.Status.Conditions, "HooksSkipped")
		return c != nil && strings.Contains(c.Message, "pre-deploy hook late"), fmt.Sprintf("conditions=%v", ps.Status.Conditions)
	})
	a.merge(t, a.openPR(t, bundle, "prod"))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
}

// TestStep_HookPrivilegedRefused: a hook Pod with a privileged container
// breaks Pod Security baseline (the default --hook-pod-security-level): the
// HookRun fails without a Job, and so does the step.
//
// Covers HOOK-PRIV-01.
func TestStep_HookPrivilegedRefused(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	p := a.pipeline(nil)
	p.Spec.Environments[0].Hooks = []v1alpha1.HookSpec{{Name: "migrate", Phase: "pre",
		Job: hookJobWith(t, `echo hi`, "", nil, map[string]interface{}{"securityContext": map[string]interface{}{"privileged": true}})}}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	name := graph.HookRunName(pipelineName, bundle, "prod", "pre", "migrate")
	hr := waitHookRun(t, e, a.ns, name, v1alpha1.HookRunFailed)
	assert.Contains(t, hr.Status.Message, `violates Pod Security "baseline"`)
	assert.Contains(t, hr.Status.Message, "--hook-pod-security-level")
	_, err := e.Kube.BatchV1().Jobs(a.ns).Get(ctx, name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "no Job: %v", err)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", 2*time.Minute)
	a.fileHas(t, "prod", fixtures.V1, "prod in git")
}

// TestStep_HookForgedHookRunIgnored: a HookRun made by hand for a Bundle,
// with its selector labels and the Bundle's UID, is refused by the chart's
// graph-objects policy (only kro and kardinal create HookRuns, #1544). Made
// anyway by an identity the policy admits, the Graph ServiceAccount as kro
// uses it, but without kro's kro.run/node-id label, it never gets a Job or a
// status (the controller's own check, QA #1493 round 2), while the Graph's
// own hook runs and the environment is promoted.
//
// Covers HOOK-FORGED-01.
func TestStep_HookForgedHookRunIgnored(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	p := a.pipeline(nil)
	p.Spec.Environments[0].Hooks = []v1alpha1.HookSpec{{Name: "migrate", Phase: "pre", Job: hookJob(t, `echo migrated`, "")}}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	var b v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: bundle}, &b))

	forged := &v1alpha1.HookRun{
		ObjectMeta: metav1.ObjectMeta{Name: "forged-" + bundle, Namespace: a.ns, Labels: map[string]string{
			"kardinal.io/pipeline": pipelineName, "kardinal.io/bundle": bundle, "kardinal.io/environment": "test",
			graph.LabelHookPhase: "pre", graph.LabelHook: "migrate", graph.LabelBundleUID: string(b.UID)}},
		Spec: v1alpha1.HookRunSpec{PipelineName: pipelineName, BundleName: bundle, Environment: "test",
			Hook: "migrate", Phase: "pre", Job: hookJob(t, `echo forged`, "")},
	}
	err := e.Client.Create(ctx, forged.DeepCopy())
	require.Error(t, err, "the cluster admin cannot forge a HookRun")
	assert.True(t, apierrors.IsForbidden(err), "%v", err)
	assert.Contains(t, err.Error(), "only kardinal (the promotion Graph or the controller) creates or changes this object")
	// kro's path: impersonating the namespace's Graph ServiceAccount. Its
	// RoleBinding is the controller's to create with the Bundle's Graph, so
	// wait for the Graph's own HookRun (kro got through) and retry while
	// the API server's RBAC cache catches up.
	own := graph.HookRunName(pipelineName, bundle, "test", "pre", "migrate")
	framework.Eventually(t, promoteTimeout, "the Graph's own HookRun", func(ctx context.Context) (bool, string) {
		_, ok, err := hookRun(ctx, e, a.ns, own)
		return err == nil && ok, fmt.Sprint(err)
	})
	asGraph := impersonated(t, e, "system:serviceaccount:"+a.ns+":kardinal-graph",
		"system:serviceaccounts", "system:serviceaccounts:"+a.ns, "system:authenticated")
	framework.Eventually(t, time.Minute, "kro's identity creates the forged HookRun", func(ctx context.Context) (bool, string) {
		err := asGraph.Create(ctx, forged.DeepCopy())
		return err == nil || apierrors.IsAlreadyExists(err), fmt.Sprint(err)
	})

	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	waitHookRun(t, e, a.ns, own, v1alpha1.HookRunSucceeded)
	framework.Consistently(t, 10*time.Second, "the forged HookRun gets no Job and no status", func(ctx context.Context) (bool, string) {
		_, err := e.Kube.BatchV1().Jobs(a.ns).Get(ctx, forged.Name, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("job: %v", err)
		}
		hr, ok, err := hookRun(ctx, e, a.ns, forged.Name)
		if err != nil || !ok {
			return false, fmt.Sprint(err)
		}
		return hr.Status.Phase == "" && len(hr.Finalizers) == 0, fmt.Sprintf("phase=%q finalizers=%v", hr.Status.Phase, hr.Finalizers)
	})
}

// impersonated is a client acting as user in groups (the test's cluster
// admin may impersonate), for an identity an admission policy admits.
func impersonated(t *testing.T, e *framework.Env, user string, groups ...string) client.Client {
	t.Helper()
	cfg := rest.CopyConfig(e.Config)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: groups}
	c, err := client.New(cfg, client.Options{Scheme: e.Client.Scheme()})
	require.NoError(t, err)
	return c
}

// TestStep_HooksRefusedInCompactGraph: the compact Graph shape does not
// carry hooks, so a Pipeline with hooks and kardinal.io/graph-shape: compact
// is Ready=False naming them, and its Bundle fails with GraphBuildFailed
// instead of promoting without its hooks.
//
// Covers HOOK-COMPACT-01.
func TestStep_HooksRefusedInCompactGraph(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	p := a.pipeline(nil)
	p.Annotations = map[string]string{graph.AnnotationGraphShape: graph.GraphShapeCompact}
	p.Spec.Environments[0].Hooks = []v1alpha1.HookSpec{{Name: "migrate", Phase: "pre", Job: hookJob(t, `echo migrated`, "")}}
	a.apply(t, p)
	const feature = "pre- and post-deploy hooks (spec.environments[].hooks)"
	e.WaitPipeline(t, a.ns, pipelineName, time.Minute, "Ready=False naming hooks", func(p *v1alpha1.Pipeline) bool {
		c := meta.FindStatusCondition(p.Status.Conditions, "Ready")
		return c != nil && c.Status == metav1.ConditionFalse && strings.Contains(c.Message, feature)
	})
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitBundle(t, a.ns, bundle, time.Minute, "Failed with GraphBuildFailed", failedWith("GraphBuildFailed", feature))
	_, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
	require.NoError(t, err)
	assert.False(t, ok, "nothing promoted without its hooks")
	a.fileHas(t, "test", fixtures.V1, "test in git")
}

// TestStep_HookDeletedWhileRunningRunsOnce: a pre-hook HookRun deleted
// (by the garbage collector) while its Job runs does not run the migration
// twice. The finalizer holds
// the HookRun until the Job ends and records its result, the step keeps the
// result in status.hookRecords, and the HookRun the Graph applies again
// takes the recorded result without a Job (regression, #1544 review).
//
// Covers HOOK-RECREATE-01.
func TestStep_HookDeletedWhileRunningRunsOnce(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	p := a.pipeline(nil)
	p.Spec.Environments[0].Hooks = []v1alpha1.HookSpec{{Name: "migrate", Phase: "pre", Job: hookJob(t, `sleep 20; echo migrated`, "")}}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	name := graph.HookRunName(pipelineName, bundle, "test", "pre", "migrate")
	first := waitHookRun(t, e, a.ns, name, v1alpha1.HookRunRunning)
	// Only kardinal, kro, the garbage collector and the namespace controller
	// delete HookRuns (graph-objects policy, #1544): the cluster admin is
	// refused, and the delete comes from the garbage collector, as when the
	// Graph drops the HookRun or its Bundle goes.
	err := e.Client.Delete(ctx, first.DeepCopy())
	require.Error(t, err, "a user cannot delete a HookRun")
	assert.True(t, apierrors.IsForbidden(err), "%v", err)
	assert.Contains(t, err.Error(), "HookRuns record that a hook ran; only kardinal deletes them")
	require.NoError(t, impersonated(t, e, "system:serviceaccount:kube-system:generic-garbage-collector",
		"system:serviceaccounts", "system:serviceaccounts:kube-system", "system:authenticated").Delete(ctx, first))

	var again *v1alpha1.HookRun
	framework.Eventually(t, 3*time.Minute, "the HookRun applied again", func(ctx context.Context) (bool, string) {
		hr, ok, err := hookRun(ctx, e, a.ns, name)
		if err != nil || !ok {
			return false, fmt.Sprint(err)
		}
		again = hr
		return hr.UID != first.UID && hr.Status.Phase != "", fmt.Sprintf("uid=%s phase=%q", hr.UID, hr.Status.Phase)
	})
	assert.Equal(t, v1alpha1.HookRunSucceeded, again.Status.Phase, again.Status.Message)
	assert.Contains(t, again.Status.Message, "not run again")
	assert.Empty(t, again.Status.JobUID, "no second Job")
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	require.NotEmpty(t, ps.Status.HookRecords)
	assert.Equal(t, "Succeeded", ps.Status.HookRecords[0].Result)
}
