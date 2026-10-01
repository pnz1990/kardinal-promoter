//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// gitCredentialMissing is the PromotionStep condition, and the reason of its
// Warning Event, for a git step the remote refused while git had no token.
const gitCredentialMissing = "GitCredentialMissing"

// rfCredentialEvents lists the GitCredentialMissing Events of step ps.
func rfCredentialEvents(ctx context.Context, e *framework.Env, ps *v1alpha1.PromotionStep) ([]corev1.Event, error) {
	var list corev1.EventList
	if err := e.Client.List(ctx, &list, client.InNamespace(ps.Namespace)); err != nil {
		return nil, err
	}
	var out []corev1.Event
	for _, ev := range list.Items {
		if ev.InvolvedObject.Kind == "PromotionStep" && ev.InvolvedObject.Name == ps.Name && ev.Reason == gitCredentialMissing {
			out = append(out, ev)
		}
	}
	return out, nil
}

// rfOneCredentialEvent waits for the Warning Event of step ps and checks
// that it was emitted once: one Event, with no series of repeats.
func rfOneCredentialEvent(t *testing.T, e *framework.Env, ps *v1alpha1.PromotionStep, note string) {
	t.Helper()
	var evs []corev1.Event
	framework.Eventually(t, time.Minute, "GitCredentialMissing Event", func(ctx context.Context) (bool, string) {
		var err error
		if evs, err = rfCredentialEvents(ctx, e, ps); err != nil {
			return false, err.Error()
		}
		return len(evs) > 0, "no Event yet"
	})
	require.Len(t, evs, 1)
	assert.Equal(t, corev1.EventTypeWarning, evs[0].Type)
	assert.Equal(t, "env test: step git-push failed and git has no credentials: "+note, evs[0].Message)
	assert.Nil(t, evs[0].Series, "emitted once, not once per retry")
}

// rfCredentialCondition is the step's GitCredentialMissing condition.
func rfCredentialCondition(t *testing.T, ps *v1alpha1.PromotionStep) *metav1.Condition {
	t.Helper()
	cond := meta.FindStatusCondition(ps.Status.Conditions, gitCredentialMissing)
	require.NotNil(t, cond, "step %s has no %s condition: %+v", ps.Name, gitCredentialMissing, ps.Status.Conditions)
	return cond
}

// TestStep_GitSecretMissing checks a Pipeline whose spec.git.secretRef names
// a Secret that does not exist. The repo is public, so the clone works and
// the push is refused. The step message names the missing Secret after the
// git error, the step is GitCredentialMissing=True with one Warning Event, and
// it keeps retrying past the retry limit (5). Creating the Secret is enough:
// the next retry pushes and the step is Verified.
//
// Covers STEP-GITCRED-01.
func TestStep_GitSecretMissing(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	p := a.pipeline(nil)
	p.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "git-creds-missing"}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)

	note := "git Secret " + a.ns + "/git-creds-missing not found"
	ps := e.WaitStepMessage(t, a.ns, pipelineName, bundle, "test", "Promoting", "("+note+")", promoteTimeout)
	assert.Contains(t, ps.Status.Message, "step git-push: ")
	assert.Equal(t, 1, strings.Count(ps.Status.Message, "authentication required"), ps.Status.Message)
	cond := rfCredentialCondition(t, ps)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "SecretNotFound", cond.Reason)
	assert.Equal(t, note, cond.Message)
	rfOneCredentialEvent(t, e, ps, note)

	// Retry 6 comes about 4.5 minutes after the first failure (backoff 10s,
	// 20s, 40s, 1m20s, 2m). The step used to fail at that point.
	framework.Eventually(t, 7*time.Minute, "retry 6 of the push", func(ctx context.Context) (bool, string) {
		got, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
		if err != nil || !ok {
			return false, fmt.Sprintf("step lookup: ok=%v err=%v", ok, err)
		}
		ps = got
		if got.Status.State == "Failed" {
			return false, "Failed: " + got.Status.Message
		}
		return got.Status.State == "Promoting" && got.Status.RetryCount >= 6,
			fmt.Sprintf("state=%q retries=%d message=%q", got.Status.State, got.Status.RetryCount, got.Status.Message)
	})
	assert.Contains(t, ps.Status.Message, ", no limit while git has no credentials)")
	assert.Contains(t, ps.Status.Message, "("+note+")")
	rfOneCredentialEvent(t, e, ps, note)

	// Create the Secret from the namespace's git token; nothing else changes.
	var token corev1.Secret
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: framework.GitSecretName}, &token))
	require.NoError(t, e.Client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Name: "git-creds-missing"},
		Data:       token.Data,
	}))
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	cond = rfCredentialCondition(t, ps)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "CredentialFound", cond.Reason)
	assertEnvAt(t, a, "test", fixtures.V2)
}

// TestStep_GitSecretRefNotSet checks a Pipeline with no spec.git.secretRef
// on an HTTPS repo: the step message says the push has no credentials, the
// step is GitCredentialMissing=True with one Warning Event, and setting the
// secretRef lets the next retry push.
//
// Covers STEP-GITCRED-02.
func TestStep_GitSecretRefNotSet(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	p := a.pipeline(nil)
	p.Spec.Git.SecretRef = nil
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)

	note := "spec.git.secretRef is not set, so the push has no credentials"
	ps := e.WaitStepMessage(t, a.ns, pipelineName, bundle, "test", "Promoting", "("+note+")", promoteTimeout)
	assert.Contains(t, ps.Status.Message, "step git-push: ")
	cond := rfCredentialCondition(t, ps)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "SecretRefNotSet", cond.Reason)
	rfOneCredentialEvent(t, e, ps, note)

	var cur v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &cur))
	patch := client.MergeFrom(cur.DeepCopy())
	cur.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: framework.GitSecretName}
	require.NoError(t, e.Client.Patch(ctx, &cur, patch))
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	cond = rfCredentialCondition(t, ps)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assertEnvAt(t, a, "test", fixtures.V2)
}

// rfAudit lists the AuditEvents with action in ns, by environment.
func rfAudit(ctx context.Context, e *framework.Env, ns, action string) ([]v1alpha1.AuditEvent, error) {
	var list v1alpha1.AuditEventList
	if err := e.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/action": action}); err != nil {
		return nil, err
	}
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Spec.Environment > list.Items[j].Spec.Environment
	})
	return list.Items, nil
}

// rfDescribeAudit is each event's bundle/environment, for failure messages.
func rfDescribeAudit(evs []v1alpha1.AuditEvent) string {
	out := make([]string, len(evs))
	for i, ae := range evs {
		out[i] = ae.Spec.BundleName + "/" + ae.Spec.Environment
	}
	return fmt.Sprintf("%v", out)
}

// TestRollback_SucceededAuditEvent checks the RollbackSucceeded AuditEvent:
// when `kardinal rollback` restores Bundle A after B, each step of the
// rollback Bundle that reaches Verified (test, then prod) writes one, besides
// PromotionSucceeded, naming the restored and the replaced Bundle. The
// forward Bundles write none, and kardinal get auditevents lists them.
//
// Covers RB-AUDIT-02.
func TestRollback_SucceededAuditEvent(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(nil))

	bA := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bA, "prod", "Verified", 2*promoteTimeout)
	bB := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitStepState(t, a.ns, pipelineName, bB, "prod", "Verified", 2*promoteTimeout)
	evs, err := rfAudit(ctx, e, a.ns, "RollbackSucceeded")
	require.NoError(t, err)
	assert.Empty(t, evs, "forward promotions write no RollbackSucceeded: %s", rfDescribeAudit(evs))

	r := c.Run(a.ns, "rollback", pipelineName, "--env", "prod")
	require.Equal(t, 0, r.Code, r.Output())
	m := regexp.MustCompile(`Bundle (podinfo-rollback-[a-z0-9]{5}) created`).FindStringSubmatch(r.Stdout)
	require.NotNil(t, m, "rollback output:\n%s", r.Stdout)
	bR := m[1]
	e.WaitStepState(t, a.ns, pipelineName, bR, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bR, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)

	framework.Eventually(t, time.Minute, "RollbackSucceeded in test and prod", func(ctx context.Context) (bool, string) {
		var err error
		if evs, err = rfAudit(ctx, e, a.ns, "RollbackSucceeded"); err != nil {
			return false, err.Error()
		}
		return len(evs) >= 2, rfDescribeAudit(evs)
	})
	require.Len(t, evs, 2, rfDescribeAudit(evs))
	for i, env := range []string{"test", "prod"} {
		ae := evs[i]
		assert.Equal(t, env, ae.Spec.Environment)
		assert.Equal(t, bR, ae.Spec.BundleName)
		assert.Equal(t, pipelineName, ae.Spec.PipelineName)
		assert.Equal(t, "Success", ae.Spec.Outcome)
		assert.Contains(t, ae.Spec.Message,
			"rollback Bundle "+bR+" (artifacts of "+bA+", rolled back from "+bB+") verified in "+env+": ")
	}
	succeeded, err := rfAudit(ctx, e, a.ns, "PromotionSucceeded")
	require.NoError(t, err)
	var forR []string
	for _, ae := range succeeded {
		if ae.Spec.BundleName == bR {
			forR = append(forR, ae.Spec.Environment)
		}
	}
	assert.Equal(t, []string{"test", "prod"}, forR, "PromotionSucceeded is still written")

	framework.Consistently(t, 30*time.Second, "one RollbackSucceeded per step", func(ctx context.Context) (bool, string) {
		got, err := rfAudit(ctx, e, a.ns, "RollbackSucceeded")
		if err != nil {
			return false, err.Error()
		}
		return len(got) == 2, rfDescribeAudit(got)
	})

	out := c.Must(a.ns, "get", "auditevents", "--bundle", bR)
	var envs []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 4 && f[4] == "RollbackSucceeded" {
			envs = append(envs, f[3])
		}
	}
	assert.ElementsMatch(t, []string{"test", "prod"}, envs, "kardinal get auditevents:\n%s", out)
}
