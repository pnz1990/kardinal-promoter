// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package promotionstep_test

import (
	"context"
	"crypto"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt/receipttest"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// receiptFixture is a step of an agent's Bundle through an approval gate
// made from a template, with receipts on.
type receiptFixture struct {
	c    client.Client
	r    *promotionstep.Reconciler
	step types.NamespacedName
	gate types.NamespacedName
}

func newReceiptFixture(t *testing.T, receipts bool, mutate func(*v1alpha1.Bundle, *v1alpha1.PromotionStep)) receiptFixture {
	t.Helper()
	evaluated := metav1.NewTime(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC))
	tmpl := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "agents-approve", Namespace: "default", UID: "tmpl-uid", Generation: 3},
		Spec: v1alpha1.PolicyGateSpec{Expression: "approvals.count >= 1",
			Approval: &v1alpha1.GateApprovalPolicy{Required: 1}},
	}
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-1-test-agents-approve", Namespace: "default", Labels: map[string]string{
			"kardinal.io/gate-template": "agents-approve", graph.LabelGateTemplateNamespace: "default"}},
		Spec: tmpl.Spec,
		Status: v1alpha1.PolicyGateStatus{Ready: true, Reason: "approved: 1 of 1 (version 1.5.0)", LastEvaluatedAt: &evaluated,
			Approvals: []v1alpha1.GateApprovalStatus{{User: "carol@example.com", Decision: "approve", Counted: true,
				Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "carol@example.com"}}}},
	}
	step := makeStep("bundle-1-test", "nginx-demo", "bundle-1", "test")
	step.UID = "step-uid"
	step.Labels = map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": "bundle-1", "kardinal.io/environment": "test"}
	step.Spec.RequiredGates = []string{gate.Name}
	bundle := makeBundle("bundle-1", "nginx-demo")
	bundle.UID = "bundle-uid"
	bundle.Status.Version = "1.5.0"
	bundle.Spec.Actor = &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:fixer",
		Agent: "fixer", Sponsor: "alice@example.com"}
	if mutate != nil {
		mutate(bundle, step)
	}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.PolicyGate{}, &v1alpha1.PromotionReceipt{},
	).WithObjects(step, makePipeline("nginx-demo"), bundle, tmpl, gate).Build()
	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{merged: true, prURL: "https://github.com/test/repo/pull/1", prNumber: 1},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}
	if receipts {
		r.Receipts = &promotionstep.ReceiptConfig{ClusterID: "cluster-1", ControllerVersion: "v0.13.0"}
	}
	return receiptFixture{c: c, r: r, step: client.ObjectKeyFromObject(step), gate: client.ObjectKeyFromObject(gate)}
}

func (f receiptFixture) reconcile(t *testing.T) *v1alpha1.PromotionStep {
	t.Helper()
	_, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: f.step})
	require.NoError(t, err)
	var s v1alpha1.PromotionStep
	require.NoError(t, f.c.Get(context.Background(), f.step, &s))
	return &s
}

func (f receiptFixture) toVerified(t *testing.T) *v1alpha1.PromotionStep {
	t.Helper()
	var s *v1alpha1.PromotionStep
	for i := 0; i < 20; i++ {
		s = f.reconcile(t)
		if s.Status.State == "Verified" || s.Status.State == "Failed" {
			break
		}
	}
	require.Equal(t, "Verified", s.Status.State, s.Status.Message)
	return s
}

func (f receiptFixture) receipt(t *testing.T) (*v1alpha1.PromotionReceipt, *receipt.Statement) {
	t.Helper()
	var rc v1alpha1.PromotionReceipt
	var list v1alpha1.PromotionReceiptList
	require.NoError(t, f.c.List(context.Background(), &list, client.InNamespace("default"),
		client.MatchingLabels{"kardinal.io/bundle": "bundle-1", "kardinal.io/environment": "test"}))
	require.Len(t, list.Items, 1, "one receipt of the promotion")
	rc = list.Items[0]
	st, err := receipt.Parse([]byte(rc.Spec.Statement))
	require.NoError(t, err)
	assert.Equal(t, receipt.Digest([]byte(rc.Spec.Statement)), rc.Spec.StatementDigest)
	return &rc, st
}

// TestReceiptJourney_AgentThroughApprovalGate: an agent's version promoted
// through an approval gate yields a receipt naming the agent and its
// sponsor, the approver, and the gate as it was when the step started: the
// gate turning false afterwards (a schedule gate at the weekend) does not
// change it. The step clears its snapshot and outbox once the receipt
// exists, and the receipt has no owner, so retiring the Bundle and the step
// leaves it.
//
// Covers RECEIPT-JOURNEY-01.
func TestReceiptJourney_AgentThroughApprovalGate(t *testing.T) {
	f := newReceiptFixture(t, true, nil)
	s := f.reconcile(t)
	require.Equal(t, "Promoting", s.Status.State)
	require.NotNil(t, s.Status.Decision, "snapshot in the patch that moves to Promoting")
	require.Len(t, s.Status.Decision.Gates, 1)
	assert.Equal(t, "default/agents-approve", s.Status.Decision.Gates[0].Template)
	assert.Equal(t, "tmpl-uid", s.Status.Decision.Gates[0].TemplateUID)
	assert.Equal(t, int64(3), s.Status.Decision.Gates[0].TemplateGeneration)

	// The gate turns false after the step started.
	var gate v1alpha1.PolicyGate
	require.NoError(t, f.c.Get(context.Background(), f.gate, &gate))
	gate.Status.Ready, gate.Status.Reason = false, "blocked: weekend"
	require.NoError(t, f.c.Status().Update(context.Background(), &gate))

	s = f.toVerified(t)
	assert.Regexp(t, `^bundle-1-test-[0-9a-f]{10}$`, s.Status.Receipt)
	assert.Nil(t, s.Status.Decision, "cleared once the receipt exists")
	assert.Nil(t, s.Status.PendingReceipt)

	rc, st := f.receipt(t)
	assert.Empty(t, rc.OwnerReferences, "a receipt outlives the Bundle and the step")
	assert.Equal(t, map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": "bundle-1", promotionstep.LabelReceiptStep: "step-uid",
		"kardinal.io/environment": "test"}, rc.Labels)
	r := st.Predicate.Kardinal
	assert.True(t, r.DecisionSnapshot)
	require.NotNil(t, r.Actor)
	assert.Equal(t, "fixer", r.Actor.Agent)
	assert.Equal(t, "alice@example.com", r.Actor.Sponsor)
	require.Len(t, r.Gates, 1)
	assert.Equal(t, "pass", r.Gates[0].Result)
	assert.Equal(t, "approved: 1 of 1 (version 1.5.0)", r.Gates[0].Reason)
	assert.Equal(t, "carol@example.com", r.Gates[0].Approvals[0].Actor.Principal)
	assert.Contains(t, st.Predicate.VerifiedLevels, receipt.LevelGatesPassed)
	assert.Contains(t, st.Predicate.VerifiedLevels, receipt.LevelApproved)
	assert.Equal(t, "kardinal://cluster-1/namespaces/default/pipelines/nginx-demo/environments/test", st.Predicate.ResourceURI)
	assert.Equal(t, "bundle-uid", r.Version.BundleUID)
	require.Len(t, st.Subject, 1, "no image digest: the Bundle's own spec is the subject")
	assert.Equal(t, "kardinal://cluster-1/namespaces/default/bundles/bundle-1", st.Subject[0].Name)

	// Retire: delete the step and the Bundle; the receipt stays.
	require.NoError(t, f.c.Delete(context.Background(), s))
	require.NoError(t, f.c.Delete(context.Background(), &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "bundle-1", Namespace: "default"}}))
	f.receipt(t)
}

// TestReceiptJourney_Rollback: a rollback Bundle's receipt names the Bundle
// it restores.
//
// Covers RECEIPT-JOURNEY-01.
func TestReceiptJourney_Rollback(t *testing.T) {
	f := newReceiptFixture(t, true, func(b *v1alpha1.Bundle, _ *v1alpha1.PromotionStep) {
		b.Spec.Provenance = &v1alpha1.BundleProvenance{RollbackOf: "bundle-0"}
	})
	f.toVerified(t)
	_, st := f.receipt(t)
	assert.Equal(t, "bundle-0", st.Predicate.Kardinal.RollbackOf)
}

// TestReceiptJourney_StartedBeforeUpgrade: a step that left Pending before
// receipts were on has no snapshot; its receipt reads the gates at
// Verified and says so (decisionSnapshot false).
//
// Covers RECEIPT-JOURNEY-02.
func TestReceiptJourney_StartedBeforeUpgrade(t *testing.T) {
	f := newReceiptFixture(t, false, nil)
	s := f.reconcile(t)
	require.Equal(t, "Promoting", s.Status.State)
	assert.Nil(t, s.Status.Decision, "no snapshot while receipts are off")
	f.r.Receipts = &promotionstep.ReceiptConfig{ClusterID: "cluster-1"}
	f.toVerified(t)
	_, st := f.receipt(t)
	assert.False(t, st.Predicate.Kardinal.DecisionSnapshot)
	require.Len(t, st.Predicate.Kardinal.Gates, 1)
	assert.Equal(t, "pass", st.Predicate.Kardinal.Gates[0].Result)
}

// TestReceiptJourney_Off: with receipts off a step takes no snapshot and
// creates no receipt.
//
// Covers RECEIPT-JOURNEY-02.
func TestReceiptJourney_Off(t *testing.T) {
	f := newReceiptFixture(t, false, nil)
	s := f.toVerified(t)
	assert.Nil(t, s.Status.Decision)
	assert.Empty(t, s.Status.Receipt)
	var list v1alpha1.PromotionReceiptList
	require.NoError(t, f.c.List(context.Background(), &list))
	assert.Empty(t, list.Items, "no receipt")
}

// TestReceiptJourney_CannotBuild: a receipt that can never be built (the
// statement is over the size cap after every cut) does not hold the step:
// it is Verified without a PromotionReceipt, with status.receiptError, one
// ReceiptFailed AuditEvent and kardinal_receipts_failed_total{sink="build"}
// counted once (spec 21 section 9).
//
// Covers RECEIPT-JOURNEY-03.
func TestReceiptJourney_CannotBuild(t *testing.T) {
	// The version's provenance, which no cut shortens, past the cap (the fake
	// client does not enforce the CRD's bounds).
	f := newReceiptFixture(t, true, func(b *v1alpha1.Bundle, _ *v1alpha1.PromotionStep) {
		b.Spec.Provenance = &v1alpha1.BundleProvenance{CIRunURL: strings.Repeat("x", v1alpha1.MaxReceiptStatementBytes+1)}
	})
	before := testutil.ToFloat64(observability.ReceiptsFailedTotal.WithLabelValues("build"))

	s := f.toVerified(t)
	assert.Contains(t, s.Status.ReceiptError, "exceeds the size cap")
	assert.Nil(t, s.Status.PendingReceipt)
	assert.Empty(t, s.Status.Receipt)
	var list v1alpha1.PromotionReceiptList
	require.NoError(t, f.c.List(context.Background(), &list))
	assert.Empty(t, list.Items, "no receipt")
	for i := 0; i < 3; i++ {
		f.reconcile(t)
	}
	assert.Equal(t, before+1, testutil.ToFloat64(observability.ReceiptsFailedTotal.WithLabelValues("build")), "counted once")
	var events v1alpha1.AuditEventList
	require.NoError(t, f.c.List(context.Background(), &events, client.InNamespace("default"),
		client.MatchingLabels{"kardinal.io/action": "ReceiptFailed"}))
	require.Len(t, events.Items, 1)
	assert.Equal(t, "Failure", events.Items[0].Spec.Outcome)
	assert.Contains(t, events.Items[0].Spec.Message, "no promotion receipt")
}

// TestReceiptJourney_TemplateAsTranslated (#1951 finding 6): the template
// is edited after the Bundle starts, and its gate instance keeps the
// expression of the generation it was translated from (#1360). The receipt
// names that generation and its digest, as the Graph builder stamped them on
// the instance, not the template's current one.
//
// Covers RECEIPT-JOURNEY-04.
func TestReceiptJourney_TemplateAsTranslated(t *testing.T) {
	f := newReceiptFixture(t, true, nil)
	ctx := context.Background()
	var tmpl, g v1alpha1.PolicyGate
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "agents-approve"}, &tmpl))
	require.NoError(t, f.c.Get(ctx, f.gate, &g))
	translated := graph.TemplateDigest(tmpl)
	g.Annotations = map[string]string{graph.AnnotationGateTemplateUID: "tmpl-uid",
		graph.AnnotationGateTemplateGeneration: "3", graph.AnnotationGateTemplateDigest: translated}
	require.NoError(t, f.c.Update(ctx, &g))

	tmpl.Spec.Expression = "approvals.count >= 2"
	tmpl.Generation = 4
	require.NoError(t, f.c.Update(ctx, &tmpl))

	f.toVerified(t)
	_, st := f.receipt(t)
	require.Len(t, st.Predicate.Kardinal.Gates, 1)
	gate := st.Predicate.Kardinal.Gates[0]
	assert.Equal(t, "approvals.count >= 1", gate.Expression, "the instance's expression")
	assert.Equal(t, int64(3), gate.TemplateGeneration, "the generation evaluated, not the template's current 4")
	assert.Equal(t, strings.TrimPrefix(translated, "sha256:"), gate.TemplateDigest["sha256"])
	assert.NotEqual(t, strings.TrimPrefix(graph.TemplateDigest(tmpl), "sha256:"), gate.TemplateDigest["sha256"])
}

// TestReceiptJourney_NewestImageVerification (#1951 finding 7): after a
// policy edit a Bundle has two Verified ImageVerifications; the receipt
// records the newest by creation time, though the list returns it first.
//
// Covers RECEIPT-JOURNEY-05.
func TestReceiptJourney_NewestImageVerification(t *testing.T) {
	f := newReceiptFixture(t, true, nil)
	ctx := context.Background()
	iv := func(name, signer string, created time.Time) {
		v := &v1alpha1.ImageVerification{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
			CreationTimestamp: metav1.NewTime(created),
			Labels:            map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": "bundle-1"}}}
		require.NoError(t, f.c.Create(ctx, v))
		v.Status.Phase = v1alpha1.ImageVerificationVerified
		v.Status.Images = []v1alpha1.ImageVerificationResult{{Image: "nginx", Verified: true, Signer: signer}}
		require.NoError(t, f.c.Update(ctx, v)) // the fixture has no status subresource for it
	}
	at := time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)
	iv("a-after-the-edit", "new-policy", at.Add(time.Hour))
	iv("z-before-the-edit", "old-policy", at)
	f.toVerified(t)
	_, st := f.receipt(t)
	require.Len(t, st.Predicate.Kardinal.Checks.ImageVerification, 1)
	assert.Equal(t, "new-policy", st.Predicate.Kardinal.Checks.ImageVerification[0].By)
}

// TestReceiptJourney_HeavyGatesSnapshotBounded (QA-3 B4): six required gates,
// each with 101 approvals and 101 Verdicts carrying long comments, reasons
// and summaries, would make a snapshot of megabytes, over the API server's
// request limit, and hold the step at Pending forever. The snapshot leaves
// out what the statement never reads, is cut to MaxDecisionBytes, says it was
// cut, and keeps the digest of every gate: the step reaches Promoting and
// then Verified with its receipt.
//
// Covers RECEIPT-JOURNEY-07.
func TestReceiptJourney_HeavyGatesSnapshotBounded(t *testing.T) {
	var names []string
	f := newReceiptFixture(t, true, nil)
	ctx := context.Background()
	long := strings.Repeat("x", 4000)
	evaluated := metav1.NewTime(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC))
	for i := 0; i < 6; i++ {
		g := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("heavy-%d", i), Namespace: "default"},
			Spec: v1alpha1.PolicyGateSpec{Expression: "approvals.count >= 1", Approval: &v1alpha1.GateApprovalPolicy{Required: 1}}}
		require.NoError(t, f.c.Create(ctx, g))
		g.Status = v1alpha1.PolicyGateStatus{Ready: true, Reason: "approved", LastEvaluatedAt: &evaluated}
		for j := 0; j < 101; j++ {
			g.Status.Approvals = append(g.Status.Approvals, v1alpha1.GateApprovalStatus{User: fmt.Sprintf("u%d", j),
				Decision: "approve", Counted: j == 0, Reason: long, Comment: long, FirstSeenAt: &evaluated})
			g.Status.Verdicts = append(g.Status.Verdicts, v1alpha1.GateVerdictStatus{Name: fmt.Sprintf("v%d", j),
				Verifier: "agent", Result: "pass", Counted: j == 0, Reason: long, Summary: long})
		}
		require.NoError(t, f.c.Status().Update(ctx, g))
		names = append(names, g.Name)
	}
	var ps v1alpha1.PromotionStep
	require.NoError(t, f.c.Get(ctx, f.step, &ps))
	ps.Spec.RequiredGates = append(ps.Spec.RequiredGates, names...)
	require.NoError(t, f.c.Update(ctx, &ps))

	s := f.reconcile(t)
	require.Equal(t, "Promoting", s.Status.State, s.Status.Message)
	require.NotNil(t, s.Status.Decision)
	b, err := json.Marshal(s.Status.Decision)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(b), v1alpha1.MaxDecisionBytes)
	assert.True(t, s.Status.Decision.Truncated)
	assert.Equal(t, 7, len(s.Status.Decision.Gates)+s.Status.Decision.OmittedGates, "every gate is counted")
	for _, g := range s.Status.Decision.Gates {
		for _, a := range g.Approvals {
			assert.Empty(t, a.Comment)
			assert.Nil(t, a.FirstSeenAt)
		}
		for _, v := range g.Verdicts {
			assert.Empty(t, v.Summary)
		}
	}
	s = f.toVerified(t)
	assert.Empty(t, s.Status.ReceiptError)
	_, st := f.receipt(t)
	assert.True(t, st.Predicate.Kardinal.Truncated)
}

// TestReceiptJourney_NameTakenByAnother (QA-3): a PromotionReceipt that
// already has this promotion's name but another step's UID (or another
// statement) is not adopted: the step records no receipt and says why in
// status.receiptError, so history and why never show another promotion's
// receipt as this one's.
//
// Covers RECEIPT-JOURNEY-08.
func TestReceiptJourney_NameTakenByAnother(t *testing.T) {
	f := newReceiptFixture(t, true, nil)
	ctx := context.Background()
	var ps v1alpha1.PromotionStep
	require.NoError(t, f.c.Get(ctx, f.step, &ps))
	other := &v1alpha1.PromotionReceipt{ObjectMeta: metav1.ObjectMeta{Namespace: "default",
		Name:   receipt.Name(ps.Spec.BundleName, ps.Spec.Environment, string(ps.UID)),
		Labels: map[string]string{promotionstep.LabelReceiptStep: "another-step"}},
		Spec: v1alpha1.PromotionReceiptSpec{Statement: "{}", StatementDigest: "sha256:other"}}
	require.NoError(t, f.c.Create(ctx, other))
	s := f.toVerified(t)
	for i := 0; i < 3; i++ {
		s = f.reconcile(t)
	}
	assert.Empty(t, s.Status.Receipt, "not adopted")
	assert.Nil(t, s.Status.PendingReceipt)
	assert.Contains(t, s.Status.ReceiptError, "is not this promotion's")
	var got v1alpha1.PromotionReceipt
	require.NoError(t, f.c.Get(ctx, client.ObjectKeyFromObject(other), &got))
	assert.Equal(t, "sha256:other", got.Spec.StatementDigest, "left alone")
}

// addGates adds n gates to the fixture step's required gates, the last
// passed by an override; heavy gates carry 101 approvals and Verdicts with
// 4 KiB texts.
func addGates(t *testing.T, f receiptFixture, n int, heavy bool) string {
	t.Helper()
	ctx := context.Background()
	long := strings.Repeat("x", 4000)
	evaluated := metav1.NewTime(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC))
	var names []string
	for i := 0; i < n; i++ {
		g := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("gate-%03d", i), Namespace: "default"},
			Spec: v1alpha1.PolicyGateSpec{Expression: "true"}}
		last := i == n-1
		if last {
			g.Spec.Overrides = []v1alpha1.PolicyGateOverride{{Reason: "hotfix", CreatedBy: "carol",
				ExpiresAt: metav1.NewTime(evaluated.Add(time.Hour))}}
		}
		require.NoError(t, f.c.Create(ctx, g))
		g.Status = v1alpha1.PolicyGateStatus{Ready: true, Reason: "passed", LastEvaluatedAt: &evaluated}
		if last {
			g.Status.Reason = "OVERRIDDEN by carol: hotfix"
		}
		if heavy {
			for j := 0; j < 101; j++ {
				g.Status.Approvals = append(g.Status.Approvals, v1alpha1.GateApprovalStatus{User: fmt.Sprintf("u%d", j),
					Decision: "approve", Counted: true, Reason: long})
				g.Status.Verdicts = append(g.Status.Verdicts, v1alpha1.GateVerdictStatus{Name: fmt.Sprintf("v%d", j),
					Verifier: "agent", Result: "pass", Counted: true, Reason: long, EvidenceURL: long})
			}
		}
		require.NoError(t, f.c.Status().Update(ctx, g))
		names = append(names, g.Name)
	}
	var ps v1alpha1.PromotionStep
	require.NoError(t, f.c.Get(ctx, f.step, &ps))
	ps.Spec.RequiredGates = append(ps.Spec.RequiredGates, names...)
	require.NoError(t, f.c.Update(ctx, &ps))
	return names[n-1]
}

// TestReceiptJourney_LevelsOverAllGates (QA-3 B6): the receipt's levels
// come from every required gate, before the snapshot is cut. A gate passed
// by an override and left out of the snapshot, past the 64-gate cap or past
// the size cap, still makes the receipt OVERRIDDEN, so --require
// '!OVERRIDDEN' refuses it; the snapshot and the statement name the gate
// left out.
//
// Covers RECEIPT-JOURNEY-09.
func TestReceiptJourney_LevelsOverAllGates(t *testing.T) {
	for name, tc := range map[string]struct {
		n     int
		heavy bool
	}{"gate cap": {70, false}, "size cap": {12, true}} {
		t.Run(name, func(t *testing.T) {
			f := newReceiptFixture(t, true, nil)
			overridden := addGates(t, f, tc.n, tc.heavy)
			s := f.reconcile(t)
			require.Equal(t, "Promoting", s.Status.State, s.Status.Message)
			d := s.Status.Decision
			require.NotNil(t, d)
			require.NotNil(t, d.Facts)
			assert.Equal(t, tc.n+1, d.Facts.Gates, "every gate")
			assert.True(t, d.Facts.Overridden)
			assert.Positive(t, d.OmittedGates)
			assert.Contains(t, d.OmittedGateNames, overridden, "the overridden gate is the one left out")
			for _, g := range d.Gates {
				assert.NotEqual(t, overridden, g.Name)
			}
			f.toVerified(t)
			_, st := f.receipt(t)
			assert.Contains(t, st.Predicate.VerifiedLevels, receipt.LevelOverridden, "OVERRIDDEN though its gate was left out")
			assert.Contains(t, st.Predicate.Kardinal.OmittedGateNames, overridden)
		})
	}
}

// TestReceiptJourney_OneHugeGate (QA-3): a single gate whose snapshot is
// over MaxDecisionBytes even cut (101 counted approvals and 101 counted
// Verdicts, each still 1 KiB of reason after the cuts) does not hold the
// step: it starts without a snapshot, says why in status.receiptError,
// reaches Verified, and gets no receipt.
//
// Covers RECEIPT-JOURNEY-10.
func TestReceiptJourney_OneHugeGate(t *testing.T) {
	f := newReceiptFixture(t, true, nil)
	ctx := context.Background()
	var g v1alpha1.PolicyGate
	require.NoError(t, f.c.Get(ctx, f.gate, &g))
	long := strings.Repeat("x", 4000)
	g.Status.Approvals, g.Status.Verdicts = nil, nil
	for j := 0; j < 101; j++ {
		g.Status.Approvals = append(g.Status.Approvals, v1alpha1.GateApprovalStatus{User: fmt.Sprintf("u%d", j),
			Decision: "approve", Counted: true, Reason: long})
		g.Status.Verdicts = append(g.Status.Verdicts, v1alpha1.GateVerdictStatus{Name: fmt.Sprintf("v%d", j),
			Verifier: "agent", Result: "pass", Counted: true, Reason: long, EvidenceURL: long})
	}
	require.NoError(t, f.c.Status().Update(ctx, &g))
	s := f.reconcile(t)
	require.Equal(t, "Promoting", s.Status.State, s.Status.Message)
	assert.Nil(t, s.Status.Decision)
	assert.Contains(t, s.Status.ReceiptError, "even with one gate")
	s = f.toVerified(t)
	assert.Empty(t, s.Status.Receipt)
	var list v1alpha1.PromotionReceiptList
	require.NoError(t, f.c.List(ctx, &list))
	assert.Empty(t, list.Items, "no receipt")
}

// TestReceiptJourney_TokenInProvenance (#2158): a Bundle written to the API
// server directly (kubectl apply, not the Bundle API), whose
// provenance.ciRunURL carries a token, yields a receipt without it: the
// statement has token=REDACTED and records the redaction, its digest is the
// stored statement's, and it signs and verifies.
//
// Covers RECEIPT-REDACT-01.
func TestReceiptJourney_TokenInProvenance(t *testing.T) {
	f := newReceiptFixture(t, true, func(b *v1alpha1.Bundle, _ *v1alpha1.PromotionStep) {
		b.Spec.Provenance = &v1alpha1.BundleProvenance{Author: "ci",
			CIRunURL: "https://ci.example.com/run/7?%74oken=ghp_s3cretvalue&attempt=1"}
	})
	f.toVerified(t)
	rc, st := f.receipt(t)
	assert.NotContains(t, rc.Spec.Statement, "ghp_s3cretvalue")
	assert.Equal(t, "https://ci.example.com/run/7?%74oken=REDACTED&attempt=1", st.Predicate.Kardinal.Version.Provenance.CIRunURL)
	assert.Equal(t, []string{"kardinalPromotion.version.provenance.ciRunURL"}, st.Predicate.Kardinal.Redacted)

	var b v1alpha1.Bundle
	require.NoError(t, f.c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "bundle-1"}, &b))
	assert.Contains(t, b.Spec.Provenance.CIRunURL, "ghp_s3cretvalue", "the Bundle itself is not changed")

	priv, pub, err := receipttest.Key()
	require.NoError(t, err)
	s := &receipt.Signer{Mode: v1alpha1.ReceiptSigningKey, Key: func(context.Context) (crypto.Signer, string, error) {
		k, err := receipt.ParsePrivateKey(priv)
		return k, "kardinal-system/receipt-key", err
	}}
	signed, err := s.Sign(context.Background(), []byte(rc.Spec.Statement))
	require.NoError(t, err)
	_, err = receipt.Verify(signed.Bundle, receipt.VerifyOptions{Key: pub, Statement: []byte(rc.Spec.Statement)})
	require.NoError(t, err)
}
