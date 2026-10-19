// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// staleWorld is a linear dev → staging → prod Pipeline whose environments
// are health-checked on their Deployment (the resource adapter), and whose
// workloads run a renamed image repository (kustomize newName): the health
// check cannot tell from the images which version runs (#2132).
func staleWorld(t *testing.T) (*World, *Plan) {
	t.Helper()
	return staleWorldOf(t, "kustomize", "linear")
}

// staleWorldOf is staleWorld with the combo's update strategy and topology.
func staleWorldOf(t *testing.T, update, topology string) (*World, *Plan) {
	t.Helper()
	ns := "team-a"
	c := Combo{Update: update, Health: "deployment", Gate: "cel", Topology: topology, Delivery: "direct", SCM: SCMGitHub,
		Outcome: "success", Actor: "human"}
	w := NewWorld(t, Options{SCM: c.SCM, PolicyNamespaces: []string{PolicyNS}, Actors: true})
	p := BuildPlan(c, ns, w.Now(), false)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("after %d rounds:\n%s", w.Rounds(), w.Describe(ns))
		}
	})
	require.NoError(t, p.Setup(w))
	w.MustApply(AgentFixture(ns)...)
	w.Metrics.Set(MetricQuery, metricPassing)
	reasons, err := w.Apply(p.Pipeline)
	require.NoError(t, err)
	require.Empty(t, reasons)
	w.Round()
	w.Round()
	w.GitOps.Rename(ns, "ghcr.io/acme/renamed")
	return w, p
}

// auditOf counts the AuditEvents of bundle in env by action.
func auditOf(t *testing.T, w *World, ns, bundle, env string) map[string]int {
	t.Helper()
	var list v1alpha1.AuditEventList
	require.NoError(t, w.Client.List(w.Ctx, &list, client.InNamespace(ns)))
	out := map[string]int{}
	for _, e := range list.Items {
		if e.Spec.BundleName == bundle && e.Spec.Environment == env {
			out[e.Spec.Action]++
		}
	}
	return out
}

// TestHealth_NewerPushSupersedesAHealthCheckingStep (#2132): an older
// version's step still health-checking an environment when a newer version
// pushes to it, and is Verified there, is Superseded: no Verified record and
// no PromotionSucceeded AuditEvent for a version the environment no longer
// runs. The API server throttles the older Bundle's writes (429), so the
// Bundle reconciler cannot record its supersession in time (what the scale
// suite's API-throttle chaos did); the step decides from the newer step's
// recorded push.
// Covers HEALTH-STALE-01.
func TestHealth_NewerPushSupersedesAHealthCheckingStep(t *testing.T) {
	w, p := staleWorld(t)
	ns := p.NS
	// staging's rollout is held, of 1.1.0 and of the version before it (an
	// available Deployment of any version passes a check that cannot read
	// the version from the images).
	w.GitOps.Fail(ns, "staging", "1.1.0")
	w.GitOps.Fail(ns, "staging", BaseTag)
	a := ImageBundle(ns, PipelineName, "1.1.0")
	p.Ship(t, w, a)
	require.True(t, w.RunUntil(MaxRounds, func() bool {
		s, ok := w.Steps(ns, a.Name)["staging"]
		return ok && s.Status.State == "HealthChecking"
	}), "1.1.0 is health-checking staging")
	w.Throttle(func(o client.Object) bool { return objKind(o) == "Bundle" && o.GetName() == a.Name })

	b := ImageBundle(ns, PipelineName, "1.2.0")
	p.Ship(t, w, b)
	require.True(t, w.RunUntil(MaxRounds, func() bool {
		s, ok := w.Steps(ns, b.Name)["staging"]
		return ok && s.Status.State == "Verified"
	}), "1.2.0 is Verified in staging")
	// staging now runs 1.2.0: available, and its image says nothing.
	for i := 0; i < 2*BlockRounds; i++ {
		w.Round()
	}
	s := w.Steps(ns, a.Name)["staging"]
	assert.Equal(t, "Superseded", s.Status.State, "1.1.0's staging step: %s", s.Status.Message)
	assert.Contains(t, s.Status.Message, b.Name)
	got := auditOf(t, w, ns, a.Name, "staging")
	assert.Zero(t, got["PromotionSucceeded"], "no PromotionSucceeded for 1.1.0 in staging")
	assert.Equal(t, 1, got["PromotionSuperseded"], "the started promotion's record is closed")
	assert.Equal(t, "1.2.0", p.Running(w)["staging"])
	// Once the API server takes 1.1.0's writes again, its Bundle is
	// Superseded and the step stays so.
	w.Throttle(nil)
	require.True(t, w.RunUntil(MaxRounds, func() bool { return w.BundlePhase(ns)[a.Name] == "Superseded" }))
	assert.Equal(t, "Superseded", w.Steps(ns, a.Name)["staging"].Status.State)
	assert.Equal(t, 1, auditOf(t, w, ns, a.Name, "staging")["PromotionSuperseded"], "recorded once")
}

// TestHealth_EnvironmentOverwrittenIsNotVerified (#2132): when the
// environment's files change after a step's push (another writer, here a
// person), the resource health check no longer passes on availability
// alone: the branch must still hold the step's commit with the files it
// wrote, as the Argo CD and Flux checks require (#1575).
// Covers HEALTH-STALE-02.
func TestHealth_EnvironmentOverwrittenIsNotVerified(t *testing.T) {
	w, p := staleWorld(t)
	ns := p.NS
	w.GitOps.Fail(ns, "staging", "1.1.0")
	w.GitOps.Fail(ns, "staging", BaseTag)
	a := ImageBundle(ns, PipelineName, "1.1.0")
	p.Ship(t, w, a)
	require.True(t, w.RunUntil(MaxRounds, func() bool {
		s, ok := w.Steps(ns, a.Name)["staging"]
		return ok && s.Status.State == "HealthChecking"
	}), "1.1.0 is health-checking staging")
	// A person rewrites staging's kustomization to another version.
	require.NoError(t, w.Git.Commit(p.Pipeline.Spec.Git.URL, "main", "hand edit",
		map[string]string{"environments/staging/kustomization.yaml": kustomizationAt("1.0.9")}, nil))
	for i := 0; i < 2*BlockRounds; i++ {
		w.Round()
	}
	s := w.Steps(ns, a.Name)["staging"]
	assert.NotEqual(t, "Verified", s.Status.State, "staging no longer holds 1.1.0's commit: %s", s.Status.Message)
	assert.Contains(t, s.Status.Message, "no longer holds", "the step says why it waits")
	assert.Zero(t, auditOf(t, w, ns, a.Name, "staging")["PromotionSucceeded"])
}

// kustomizationAt is an environment's kustomization pinning the app at tag.
func kustomizationAt(tag string) string {
	return "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - deployment.yaml\nimages:\n  - name: " +
		AppRepository + "\n    newTag: " + tag + "\n"
}

// TestHealth_BranchUnreadableWaits (#2137 QA): when the git host cannot be
// read (branch heads and history fail), the resource check of a workload
// whose images do not name the version waits, saying why: it never passes
// on availability alone. It passes once the branch can be read again.
// Covers HEALTH-STALE-02.
func TestHealth_BranchUnreadableWaits(t *testing.T) {
	w, p := staleWorld(t)
	ns := p.NS
	w.GitOps.Fail(ns, "staging", "1.1.0")
	w.GitOps.Fail(ns, "staging", BaseTag)
	a := ImageBundle(ns, PipelineName, "1.1.0")
	p.Ship(t, w, a)
	require.True(t, w.RunUntil(MaxRounds, func() bool {
		s, ok := w.Steps(ns, a.Name)["staging"]
		return ok && s.Status.State == "HealthChecking"
	}), "1.1.0 is health-checking staging")
	w.FailGitReads(errors.New("HTTP 503: the git host is unavailable"))
	w.GitOps.Heal(ns, "staging", "1.1.0") // the Deployment is available now
	for i := 0; i < 2*BlockRounds; i++ {
		w.Round()
	}
	s := w.Steps(ns, a.Name)["staging"]
	assert.Equal(t, "HealthChecking", s.Status.State, "it waits: %s", s.Status.Message)
	assert.Contains(t, s.Status.Message, "could not be read")
	assert.Zero(t, auditOf(t, w, ns, a.Name, "staging")["PromotionSucceeded"])

	w.FailGitReads(nil)
	require.True(t, w.RunUntil(MaxRounds, func() bool { return w.Steps(ns, a.Name)["staging"].Status.State == "Verified" }),
		"once the branch reads, it holds 1.1.0's commit: Verified")
}

// TestHealth_BranchHistoryUnreadableWaits (#2137 QA): the branch's head
// reads but its history does not, so whether the head still holds the
// step's commit is unknown: the step waits, it does not pass. Covers
// HEALTH-STALE-02.
func TestHealth_BranchHistoryUnreadableWaits(t *testing.T) {
	w, p := staleWorld(t)
	ns := p.NS
	w.GitOps.Fail(ns, "staging", "1.1.0")
	w.GitOps.Fail(ns, "staging", BaseTag)
	a := ImageBundle(ns, PipelineName, "1.1.0")
	p.Ship(t, w, a)
	require.True(t, w.RunUntil(MaxRounds, func() bool {
		s, ok := w.Steps(ns, a.Name)["staging"]
		return ok && s.Status.State == "HealthChecking"
	}), "1.1.0 is health-checking staging")
	// Another commit, outside staging's files, moves the head past the
	// step's commit: whether it still holds them takes the history.
	require.NoError(t, w.Git.Commit(p.Pipeline.Spec.Git.URL, "main", "an unrelated commit",
		map[string]string{"notes/unrelated.txt": "unrelated\n"}, nil))
	w.FailGitHistoryReads(errors.New("HTTP 503: the git host is unavailable"))
	w.GitOps.Heal(ns, "staging", "1.1.0") // the Deployment is available now
	for i := 0; i < 2*BlockRounds; i++ {
		w.Round()
	}
	s := w.Steps(ns, a.Name)["staging"]
	assert.Equal(t, "HealthChecking", s.Status.State, "it waits: %s", s.Status.Message)
	assert.Contains(t, s.Status.Message, "could not be read")
	assert.Contains(t, s.Status.Message, "read the history of", "the history read failed, not the heads read")
	assert.Zero(t, auditOf(t, w, ns, a.Name, "staging")["PromotionSucceeded"])

	w.FailGitHistoryReads(nil)
	require.True(t, w.RunUntil(MaxRounds, func() bool { return w.Steps(ns, a.Name)["staging"].Status.State == "Verified" }),
		"once the history reads, the branch holds 1.1.0's commit: Verified")
}

// TestHealth_TimeoutAfterANewerPushIsSuperseded (#2137 QA): a step whose
// health check times out after a newer version pushed to its environment is
// Superseded with a PromotionSuperseded record, not Failed: the environment
// runs the newer version, so this one's health no longer says anything.
// Covers HEALTH-STALE-01.
func TestHealth_TimeoutAfterANewerPushIsSuperseded(t *testing.T) {
	w, p := staleWorld(t)
	ns := p.NS
	for _, tag := range []string{BaseTag, "1.1.0", "1.2.0"} {
		w.GitOps.Fail(ns, "staging", tag) // staging never becomes healthy
	}
	a := ImageBundle(ns, PipelineName, "1.1.0")
	p.Ship(t, w, a)
	require.True(t, w.RunUntil(MaxRounds, func() bool {
		s, ok := w.Steps(ns, a.Name)["staging"]
		return ok && s.Status.State == "HealthChecking"
	}), "1.1.0 is health-checking staging")
	w.Throttle(func(o client.Object) bool { return objKind(o) == "Bundle" && o.GetName() == a.Name })
	b := ImageBundle(ns, PipelineName, "1.2.0")
	p.Ship(t, w, b)
	require.True(t, w.RunUntil(MaxRounds, func() bool {
		s, ok := w.Steps(ns, b.Name)["staging"]
		return ok && s.Status.State == "HealthChecking"
	}), "1.2.0 pushed to staging")
	require.True(t, w.RunUntil(MaxRounds, func() bool {
		st := w.Steps(ns, a.Name)["staging"].Status.State
		return st != "HealthChecking"
	}), "1.1.0's health check ends at its timeout")
	s := w.Steps(ns, a.Name)["staging"]
	assert.Equal(t, "Superseded", s.Status.State, "not Failed: %s", s.Status.Message)
	got := auditOf(t, w, ns, a.Name, "staging")
	assert.Equal(t, 1, got["PromotionSuperseded"])
	assert.Zero(t, got["PromotionFailed"]+got["HealthCheckFailed"]+got["PromotionSucceeded"], "%v", got)
	w.Throttle(nil)
}

// TestHealth_FleetTargetRollbackSupersedesUpstream (#2137 live:
// TestGraph_FleetTargetRolledBackFirst): a rollback of one fleet target
// pushes every environment on its way there. When it pushes dev while an
// older version is health-checking dev, dev runs the rollback's version:
// the older version's dev step is Superseded, not Verified on availability
// and not Failed at its timeout (no health alarm).
// Covers HEALTH-STALE-01.
func TestHealth_FleetTargetRollbackSupersedesUpstream(t *testing.T) {
	w, p := staleWorldOf(t, "kustomize", "fleet")
	ns := p.NS
	w.GitOps.Fail(ns, "dev", "1.1.0")
	w.GitOps.Fail(ns, "dev", BaseTag)
	a := ImageBundle(ns, PipelineName, "1.1.0")
	p.Ship(t, w, a)
	require.True(t, w.RunUntil(MaxRounds, func() bool {
		s, ok := w.Steps(ns, a.Name)["dev"]
		return ok && s.Status.State == "HealthChecking"
	}), "1.1.0 is health-checking dev")

	rb := ImageBundle(ns, PipelineName, "1.0.5")
	rb.Name = PipelineName + "-rollback-eu"
	rb.Labels = map[string]string{"kardinal.io/rollback": "true"}
	rb.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod-eu"}
	p.Ship(t, w, rb)
	require.True(t, w.RunUntil(MaxRounds, func() bool {
		s, ok := w.Steps(ns, rb.Name)["dev"]
		return ok && s.Status.State == "Verified"
	}), "the rollback pushed dev and is Verified there")
	require.True(t, w.RunUntil(MaxRounds, func() bool {
		return w.Steps(ns, a.Name)["dev"].Status.State != "HealthChecking"
	}), "1.1.0's dev check ends")
	s := w.Steps(ns, a.Name)["dev"]
	assert.Equal(t, "Superseded", s.Status.State, "1.1.0's dev step: %s", s.Status.Message)
	assert.Contains(t, s.Status.Message, rb.Name)
	got := auditOf(t, w, ns, a.Name, "dev")
	assert.Zero(t, got["PromotionSucceeded"]+got["PromotionFailed"]+got["HealthCheckFailed"], "%v", got)
	assert.Equal(t, 1, got["PromotionSuperseded"])
}

// TestHealth_RenderedBranchIsVerified (#2137 live:
// TestGraph_FleetRenderedBranches): a layout: branch environment's step
// pushes its rendered branch; the check reads that branch, not the base
// branch, and the step is Verified.
// Covers HEALTH-STALE-02.
func TestHealth_RenderedBranchIsVerified(t *testing.T) {
	w, p := staleWorldOf(t, "rendered", "linear")
	ns := p.NS
	a := ImageBundle(ns, PipelineName, "1.1.0")
	p.Ship(t, w, a)
	require.True(t, w.RunUntil(MaxRounds, func() bool { return w.BundlePhase(ns)[a.Name] == "Verified" }),
		"every rendered environment is Verified")
	for env, s := range w.Steps(ns, a.Name) {
		assert.Equal(t, "Verified", s.Status.State, "%s: %s", env, s.Status.Message)
	}
}
