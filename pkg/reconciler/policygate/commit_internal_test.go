// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// commitIV is an ImageVerification of bundle app-v1 that checked its config
// commit with result.
func commitIV(name string, created time.Time, result *kardinalv1alpha1.CommitVerificationResult) *kardinalv1alpha1.ImageVerification {
	iv := &kardinalv1alpha1.ImageVerification{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
		CreationTimestamp: metav1.NewTime(created), Labels: map[string]string{labelBundle: "app-v1", labelPipeline: "app"}},
		Spec: kardinalv1alpha1.ImageVerificationSpec{Commit: &kardinalv1alpha1.VerifiedCommit{Repo: "https://github.com/acme/config", SHA: "abc"}}}
	iv.Status.Phase = kardinalv1alpha1.ImageVerificationVerified
	iv.Status.Commit = result
	return iv
}

// commitReconciler has bundle app-v1, created by agent author (a person when
// author is "").
func commitReconciler(t *testing.T, author string, objs ...client.Object) *Reconciler {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	bundle := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default",
		Labels: map[string]string{labelPipeline: "app"}}, Spec: kardinalv1alpha1.BundleSpec{Type: "config", Pipeline: "app",
		Actor: &kardinalv1alpha1.Actor{Kind: "human", Principal: "alice@example.com"}}}
	if author != "" {
		bundle.Spec.Actor = &kardinalv1alpha1.Actor{Kind: "agent", Principal: "system:serviceaccount:ci:" + author, Agent: author, Sponsor: "alice@example.com"}
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(append(objs, bundle)...).
		WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }
	return r
}

// TestCommitContext (docs/design/20-attestations.md §3.6, §6): commit.* is
// the newest ImageVerification's commit result, and an agent's version
// whose commit its own workload signed passes commit.agent == actor.agent
// while another agent's signature, or an SCM-verified one, holds it.
func TestCommitContext(t *testing.T) {
	gate := &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "prod-gate", Namespace: "default",
		Labels: map[string]string{labelPipeline: "app", labelEnvironment: "prod", labelBundle: "app-v1"}}}
	const sameAgent = `actor.kind != "agent" || commit.agent == actor.agent`
	signedBy := func(agent string) *kardinalv1alpha1.CommitVerificationResult {
		return &kardinalv1alpha1.CommitVerificationResult{Verified: true, Method: "gitsign",
			Signer: "https://github.com/acme/" + agent + "/.github/workflows/fix.yml@refs/heads/main",
			Issuer: "https://token.actions.githubusercontent.com", Authority: "agents", Agent: agent}
	}
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		author string
		ivs    []client.Object
		expr   string
		want   bool
	}{
		{"the agent signed its own commit", "fixer", []client.Object{commitIV("v", t0, signedBy("fixer"))}, sameAgent, true},
		{"another agent signed it", "fixer", []client.Object{commitIV("v", t0, signedBy("helper"))}, sameAgent, false},
		{"the SCM verified it (no agent)", "fixer", []client.Object{commitIV("v", t0,
			&kardinalv1alpha1.CommitVerificationResult{Verified: true, Method: "scm", Signer: "fixer-bot"})}, sameAgent, false},
		{"no ImageVerification", "fixer", nil, sameAgent, false},
		{"a person's version", "", []client.Object{commitIV("v", t0, signedBy("helper"))}, sameAgent, true},
		{"the newest ImageVerification decides", "fixer", []client.Object{commitIV("old", t0, signedBy("fixer")),
			commitIV("new", t0.Add(time.Hour), signedBy("helper"))}, sameAgent, false},
		{"method and issuer", "fixer", []client.Object{commitIV("v", t0, signedBy("fixer"))},
			`commit.verified && commit.method == "gitsign" && commit.issuer == "https://token.actions.githubusercontent.com" && commit.authority == "agents"`, true},
		{"signer", "fixer", []client.Object{commitIV("v", t0, signedBy("fixer"))},
			`commit.signer.startsWith("https://github.com/acme/fixer/")`, true},
		{"not verified", "fixer", []client.Object{commitIV("v", t0,
			&kardinalv1alpha1.CommitVerificationResult{Method: "gitsign", Message: "does not verify"})}, `commit.verified`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := commitReconciler(t, tc.author, tc.ivs...)
			gate.Spec.Expression = tc.expr
			celCtx, _, err := r.buildContext(t.Context(), gate, "app-v1")
			require.NoError(t, err)
			ok, reason, err := r.eval.evaluate(t.Context(), tc.expr, celCtx)
			require.NoError(t, err, reason)
			assert.Equal(t, tc.want, ok, reason)
		})
	}

	// commit.message is not in the context: it can hold an SCM error.
	r := commitReconciler(t, "fixer", commitIV("v", t0, &kardinalv1alpha1.CommitVerificationResult{Method: "scm", Message: "SCM: 502"}))
	gate.Spec.Expression = "commit.verified"
	celCtx, _, err := r.buildContext(t.Context(), gate, "app-v1")
	require.NoError(t, err)
	assert.NotContains(t, celCtx["commit"], "message")
}

// TestImageVerificationRequests_Commit: a commit result change re-evaluates
// the gates that read commit.*.
func TestImageVerificationRequests_Commit(t *testing.T) {
	g := &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "reads-commit", Namespace: "default",
		Labels: map[string]string{labelBundle: "app-v1"}}, Spec: kardinalv1alpha1.PolicyGateSpec{Expression: "commit.verified"}}
	r := commitReconciler(t, "", g)
	iv := commitIV("v", time.Now(), &kardinalv1alpha1.CommitVerificationResult{Method: "gitsign"})
	reqs := r.imageVerificationRequests(t.Context(), iv)
	require.Len(t, reqs, 1)
	assert.Equal(t, "reads-commit", reqs[0].Name)

	verified := iv.DeepCopy()
	verified.Status.Commit = &kardinalv1alpha1.CommitVerificationResult{Verified: true, Method: "gitsign", Agent: "fixer"}
	assert.True(t, imageVerificationStatusChanged.Update(updateEvent(iv, verified)))
}
