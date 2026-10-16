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
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
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
	require.NoError(t, f.c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "bundle-1-test"}, &rc))
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
	assert.Equal(t, "bundle-1-test", s.Status.Receipt)
	assert.Nil(t, s.Status.Decision, "cleared once the receipt exists")
	assert.Nil(t, s.Status.PendingReceipt)

	rc, st := f.receipt(t)
	assert.Empty(t, rc.OwnerReferences, "a receipt outlives the Bundle and the step")
	assert.Equal(t, map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": "bundle-1",
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
	var rc v1alpha1.PromotionReceipt
	err := f.c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "bundle-1-test"}, &rc)
	assert.True(t, apierrors.IsNotFound(err))
}

// TestReceiptJourney_CannotBuild: a receipt that can never be built (the
// statement is over the size cap after every cut) does not hold the step:
// it is Verified without a PromotionReceipt, with status.receiptError, one
// ReceiptFailed AuditEvent and kardinal_receipts_failed_total{sink="build"}
// counted once (spec 21 section 9).
//
// Covers RECEIPT-JOURNEY-03.
func TestReceiptJourney_CannotBuild(t *testing.T) {
	f := newReceiptFixture(t, true, nil)
	var g v1alpha1.PolicyGate
	require.NoError(t, f.c.Get(context.Background(), f.gate, &g))
	// A counted approval's reason, which the cuts keep, past the cap (the fake
	// client does not enforce the CRD's bounds).
	g.Status.Approvals[0].Reason = strings.Repeat("x", v1alpha1.MaxReceiptStatementBytes+1)
	require.NoError(t, f.c.Status().Update(context.Background(), &g))
	before := testutil.ToFloat64(observability.ReceiptsFailedTotal.WithLabelValues("build"))

	s := f.toVerified(t)
	assert.Contains(t, s.Status.ReceiptError, "exceeds the size cap")
	assert.Nil(t, s.Status.PendingReceipt)
	assert.Empty(t, s.Status.Receipt)
	var rc v1alpha1.PromotionReceipt
	err := f.c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "bundle-1-test"}, &rc)
	assert.True(t, apierrors.IsNotFound(err), "no receipt")
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
