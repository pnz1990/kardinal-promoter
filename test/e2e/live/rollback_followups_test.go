//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
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
	assert.Contains(t, evs[0].Message, "git has no credentials", evs[0].Message)
	assert.Contains(t, evs[0].Message, note, evs[0].Message)
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
// the push is refused. The step message names the missing Secret right after
// the git error, with no newline from the git server's response between them,
// the step is GitCredentialMissing=True with one Warning Event, and
// it keeps retrying past the retry limit (5), in status.gitCredentialRetries,
// leaving status.retryCount at 0. Creating the Secret is enough: the next
// retry pushes and the step is Verified.
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
	// Gitea answers 401 with "Unauthorized\n"; the newline is trimmed.
	assert.Contains(t, ps.Status.Message, ": authentication required: Unauthorized ("+note+")")
	assert.NotContains(t, ps.Status.Message, "\n")
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
		return got.Status.State == "Promoting" && got.Status.GitCredentialRetries >= 6,
			fmt.Sprintf("state=%q credential retries=%d message=%q", got.Status.State, got.Status.GitCredentialRetries, got.Status.Message)
	})
	assert.Contains(t, ps.Status.Message, ", no limit while git has no credentials)")
	assert.Zero(t, ps.Status.RetryCount, "credential retries leave the retry limit for other errors")
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
	assert.Contains(t, ps.Status.Message, ": authentication required: Unauthorized ("+note+")")
	assert.NotContains(t, ps.Status.Message, "\n")
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

	out := c.Must(a.ns, "get", "auditevents", "--bundle", bR, "-o", "json")
	var listed []v1alpha1.AuditEvent
	require.NoError(t, json.Unmarshal([]byte(out), &listed), out)
	var envs []string
	for _, ae := range listed {
		if ae.Spec.Action == "RollbackSucceeded" {
			envs = append(envs, ae.Spec.Environment)
		}
	}
	assert.ElementsMatch(t, []string{"test", "prod"}, envs, "kardinal get auditevents -o json:\n%s", out)
}

// rfConfigValue2 is the UI message of the second config commit in
// TestRollback_MixedBundle.
const rfConfigValue2 = "from-the-second-config-commit"

// rfWaitConfigValue waits until env's Deployment in the cluster sets
// configVar to value.
func rfWaitConfigValue(t *testing.T, a *app, env, value string) {
	t.Helper()
	framework.Eventually(t, syncTimeout, env+" runs "+configVar+"="+value, func(ctx context.Context) (bool, string) {
		var d appsv1.Deployment
		if err := a.e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: fixtures.Workload(env)}, &d); err != nil {
			return false, err.Error()
		}
		envVars := d.Spec.Template.Spec.Containers[0].Env
		return slices.Contains(envVars, corev1.EnvVar{Name: configVar, Value: value}), fmt.Sprintf("env %v", envVars)
	})
}

// rfPushConfigValue2 pushes the second config commit to cfg, which sets
// configVar to rfConfigValue2 in env test, and returns it.
func rfPushConfigValue2(t *testing.T, e *framework.Env, cfg gitserver.Repo) string {
	t.Helper()
	return e.PushTree(t, cfg, "change the UI message", func(dir string) {
		file := filepath.Join(dir, fixtures.Path("test"), "deployment.yaml")
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(file, []byte(strings.ReplaceAll(string(body), configValue, rfConfigValue2)), 0o600))
	})
}

// rfRollbackBundle returns the rollback Bundle that `kardinal rollback`
// printed it created.
func rfRollbackBundle(t *testing.T, e *framework.Env, ns string, r framework.CLIResult) *v1alpha1.Bundle {
	t.Helper()
	m := regexp.MustCompile(`Bundle (podinfo-rollback-[a-z0-9]{5}) created`).FindStringSubmatch(r.Stdout)
	require.NotNil(t, m, "rollback output:\n%s", r.Stdout)
	var rb v1alpha1.Bundle
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: m[1]}, &rb))
	return &rb
}

// rfRollbackBundles lists the names of the rollback Bundles in ns.
func rfRollbackBundles(t *testing.T, e *framework.Env, ns string) []string {
	t.Helper()
	var list v1alpha1.BundleList
	require.NoError(t, e.Client.List(context.Background(), &list, client.InNamespace(ns),
		client.MatchingLabels{"kardinal.io/rollback": "true"}))
	var out []string
	for _, b := range list.Items {
		out = append(out, b.Name)
	}
	return out
}

// TestRollback_MixedBundle checks that a rollback of a mixed Bundle, which
// deploys a config commit and then its images, restores the config commit as
// well as the images. In env test:
//
//   - I1 (image V2), then M2 (mixed: image V3 and config commit c1).
//   - `kardinal rollback --to I1` is refused: an image Bundle cannot take back
//     the config commit M2 deployed, and no earlier config commit was Verified.
//     It used to roll back like an image Bundle, leaving c1 in place.
//   - K3 (config commit c2), then `kardinal rollback --to M2`: the rollback
//     Bundle is mixed, with M2's image and c1, and the Deployment runs c1's
//     config again. It used to be refused because M2 is not a config Bundle.
//
// Covers RB-MIXED-01.
func TestRollback_MixedBundle(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))

	i1 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, i1, "test", "Verified", promoteTimeout)
	cfg, c1 := a.configRepo(t, "test")
	m2 := e.CreateBundle(t, a.ns, pipelineName, "--type", "mixed", "--image", imageV3,
		"--config-commit", c1, "--config-repo", cfg.CloneURL)
	e.WaitStepState(t, a.ns, pipelineName, m2, "test", "Verified", promoteTimeout)
	require.True(t, a.configDeployed(t, "test"), "M2 deploys c1")
	a.waitConfigRunning(t, "test")
	assertEnvAt(t, a, "test", fixtures.V3)

	// An image Bundle cannot take back the config commit M2 deployed.
	r := c.Run(a.ns, "rollback", pipelineName, "--env", "test", "--to", i1)
	assert.NotEqual(t, 0, r.Code, "a rollback to image Bundle %s must be refused:\n%s", i1, r.Output())
	assert.Contains(t, r.Output(), "bundle "+i1+" is an image Bundle and cannot restore the config commit of ")
	assert.Contains(t, r.Output(), "that the deployed mixed bundle "+m2+" changed to "+c1[:7]+
		", and no earlier config commit was Verified in test; pick a config or mixed Bundle with --to")
	require.Empty(t, rfRollbackBundles(t, e, a.ns), "the refused rollback creates no Bundle:\n%s", r.Output())

	// K3 deploys a second config commit, c2.
	c2 := rfPushConfigValue2(t, e, cfg)
	k3 := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", c2, "--config-repo", cfg.CloneURL)
	e.WaitStepState(t, a.ns, pipelineName, k3, "test", "Verified", promoteTimeout)
	require.False(t, a.configDeployed(t, "test"), "K3 replaces c1 with c2")
	rfWaitConfigValue(t, a, "test", rfConfigValue2)
	assertEnvAt(t, a, "test", fixtures.V3)

	// A rollback to M2 is a mixed Bundle with M2's image and c1.
	r = c.Run(a.ns, "rollback", pipelineName, "--env", "test", "--to", m2)
	require.Equal(t, 0, r.Code, "a rollback of config Bundle %s to mixed Bundle %s:\n%s", k3, m2, r.Output())
	rb := rfRollbackBundle(t, e, a.ns, r)
	assert.Equal(t, "mixed", rb.Spec.Type, "the rollback keeps M2's type")
	require.Len(t, rb.Spec.Images, 1)
	assert.Equal(t, fixtures.Image, rb.Spec.Images[0].Repository)
	assert.Equal(t, fixtures.V3, rb.Spec.Images[0].Tag)
	require.NotNil(t, rb.Spec.ConfigRef, "the rollback carries M2's config commit")
	assert.Equal(t, c1, rb.Spec.ConfigRef.CommitSHA)
	require.NotNil(t, rb.Spec.Provenance)
	assert.Equal(t, m2, rb.Spec.Provenance.RollbackOf)

	ps := e.WaitStepState(t, a.ns, pipelineName, rb.Name, "test", "Verified", promoteTimeout)
	var names []string
	for _, s := range ps.Status.Steps {
		names = append(names, s.Name)
	}
	merge, setImage := slices.Index(names, "config-merge"), slices.Index(names, "kustomize-set-image")
	require.NotEqual(t, -1, merge, "the rollback merges c1: %v", names)
	require.NotEqual(t, -1, setImage, "the rollback sets the image: %v", names)
	assert.Less(t, merge, setImage, "the rollback merges c1, then sets the image: %v", names)
	assert.True(t, a.configDeployed(t, "test"), "the rollback deploys c1 again")
	a.waitConfigRunning(t, "test")
	assertEnvAt(t, a, "test", fixtures.V3)
}

// TestRollback_MixedBundleNewestSources checks that a rollback of a mixed
// Bundle with no --to puts back what the environment ran before it, whichever
// Bundles deployed it. In env test:
//
//   - M1 (mixed: image V2 and config commit c1), I2 (image V3), then M3
//     (mixed: image V1 and config commit c2).
//   - `kardinal rollback` with no --to rolls M3 back to I2's image V3 and
//     M1's commit c1, in a mixed rollback Bundle of I2. It used to consider
//     only mixed Bundles, so it went back to M1 and deployed V2, skipping I2.
//
// The automatic rollbacks (RollbackPolicy, onHealthFailure=rollback) use the
// same planner, lifecycle.PlanRollback; its unit tests run each case both ways.
//
// Covers RB-MIXED-02.
func TestRollback_MixedBundleNewestSources(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))

	cfg, c1 := a.configRepo(t, "test")
	m1 := e.CreateBundle(t, a.ns, pipelineName, "--type", "mixed", "--image", imageV2,
		"--config-commit", c1, "--config-repo", cfg.CloneURL)
	e.WaitStepState(t, a.ns, pipelineName, m1, "test", "Verified", promoteTimeout)
	a.waitConfigRunning(t, "test")
	assertEnvAt(t, a, "test", fixtures.V2)
	i2 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV3)
	e.WaitStepState(t, a.ns, pipelineName, i2, "test", "Verified", promoteTimeout)
	assertEnvAt(t, a, "test", fixtures.V3)
	c2 := rfPushConfigValue2(t, e, cfg)
	m3 := e.CreateBundle(t, a.ns, pipelineName, "--type", "mixed", "--image", imageV1,
		"--config-commit", c2, "--config-repo", cfg.CloneURL)
	e.WaitStepState(t, a.ns, pipelineName, m3, "test", "Verified", promoteTimeout)
	rfWaitConfigValue(t, a, "test", rfConfigValue2)
	assertEnvAt(t, a, "test", fixtures.V1)

	r := c.Run(a.ns, "rollback", pipelineName, "--env", "test")
	require.Equal(t, 0, r.Code, "a rollback of mixed Bundle %s:\n%s", m3, r.Output())
	assert.Contains(t, r.Stdout, "from "+m3+" to "+i2+" (", "the target is I2, the newest Bundle before M3")
	rb := rfRollbackBundle(t, e, a.ns, r)
	assert.Equal(t, "mixed", rb.Spec.Type, "the rollback deploys an image and a config commit")
	require.Len(t, rb.Spec.Images, 1)
	assert.Equal(t, fixtures.Image, rb.Spec.Images[0].Repository)
	assert.Equal(t, fixtures.V3, rb.Spec.Images[0].Tag, "I2's image, the newest before M3, not M1's")
	require.NotNil(t, rb.Spec.ConfigRef, "the rollback puts back the config commit M3 changed")
	assert.Equal(t, c1, rb.Spec.ConfigRef.CommitSHA, "M1's commit, the newest before M3")
	require.NotNil(t, rb.Spec.Provenance)
	assert.Equal(t, i2, rb.Spec.Provenance.RollbackOf)
	assert.Equal(t, m3, rb.Annotations["kardinal.io/rollback-from"])

	e.WaitStepState(t, a.ns, pipelineName, rb.Name, "test", "Verified", promoteTimeout)
	assert.True(t, a.configDeployed(t, "test"), "the rollback deploys c1 again")
	a.waitConfigRunning(t, "test")
	assertEnvAt(t, a, "test", fixtures.V3)
}
