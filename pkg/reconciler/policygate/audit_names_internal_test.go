// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestGateAuditNames_PerBundleUID (#2380): a gate's audit records are named
// and stamped with its Bundle's UID. Two Bundles of one name (one deleted
// and created again) get two names for the same record, so neither is
// dropped as AlreadyExists; one Bundle gets the same name again, so a
// retried write stays one record. A verdict's appearances name its records
// too. With no UID there is no record: a v0.14 gate record always names one
// (QA-5 on #2395).
func TestGateAuditNames_PerBundleUID(t *testing.T) {
	r := &Reconciler{NowFn: func() time.Time { return time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC) }}
	gate := &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "prod-cab", Namespace: "default",
		Labels: map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/bundle": "app-v2",
			"kardinal.io/environment": "prod", "kardinal.io/gate-name": "cab"}}}
	seen := metav1.NewTime(time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC))
	approval := kardinalv1alpha1.GateApprovalStatus{User: "alice", Decision: "approve", FirstSeenAt: &seen}
	override := &kardinalv1alpha1.PolicyGateOverride{Reason: "hotfix", ExpiresAt: metav1.NewTime(seen.Add(time.Hour)), CreatedBy: "bob"}
	overrideRec := kardinalv1alpha1.OverrideRecord{Key: overrideKey(override), FirstSeen: seen}
	verdict := kardinalv1alpha1.GateVerdictStatus{Name: "r1", Result: "pass", Verifier: "agent qa for bob"}

	builders := map[string]func(uid string, appearance int32) kardinalv1alpha1.PendingAuditEvent{
		"ApprovalRecorded": func(uid string, _ int32) kardinalv1alpha1.PendingAuditEvent {
			e, ok := r.approvalAuditEntry(gate, approval, auditActionApprovalRecorded, uid)
			require.True(t, ok)
			return e
		},
		"GateOverridden": func(uid string, _ int32) kardinalv1alpha1.PendingAuditEvent {
			e, ok := r.overrideAuditEntry(gate, override, overrideRec, uid)
			require.True(t, ok)
			return e
		},
		"VerdictRecorded": func(uid string, n int32) kardinalv1alpha1.PendingAuditEvent {
			e, ok := r.verdictAuditEntry(gate, verdict, nil, auditActionVerdictRecorded, uid, n)
			require.True(t, ok)
			return e
		},
		"GateEvaluated": func(uid string, _ int32) kardinalv1alpha1.PendingAuditEvent {
			e, ok := gateAuditEntry(gate, uid, "Success", "ok", seen)
			require.True(t, ok)
			return e
		},
	}
	for action, build := range builders {
		t.Run(action, func(t *testing.T) {
			_, written := map[string]func() (kardinalv1alpha1.PendingAuditEvent, bool){
				"ApprovalRecorded": func() (kardinalv1alpha1.PendingAuditEvent, bool) {
					return r.approvalAuditEntry(gate, approval, auditActionApprovalRecorded, "")
				},
				"GateOverridden": func() (kardinalv1alpha1.PendingAuditEvent, bool) {
					return r.overrideAuditEntry(gate, override, overrideRec, "")
				},
				"VerdictRecorded": func() (kardinalv1alpha1.PendingAuditEvent, bool) {
					return r.verdictAuditEntry(gate, verdict, nil, auditActionVerdictRecorded, "", 1)
				},
				"GateEvaluated": func() (kardinalv1alpha1.PendingAuditEvent, bool) {
					return gateAuditEntry(gate, "", "Success", "ok", seen)
				},
			}[action]()
			assert.False(t, written, "no record without the Bundle's UID")
			a, b := build("uid-a", 1), build("uid-b", 1)
			assert.Equal(t, "uid-a", a.Spec.BundleUID)
			assert.Equal(t, "uid-b", b.Spec.BundleUID)
			assert.Equal(t, build("uid-a", 1).Name, a.Name, "one Bundle, one record: the same name again")
			if action == "GateEvaluated" {
				return // named by its time, not the Bundle (GATE-AUDIT-02)
			}
			assert.NotEqual(t, a.Name, b.Name, "a Bundle created again under its name gets a record of its own")
			if action == "VerdictRecorded" {
				assert.NotEqual(t, a.Name, build("uid-a", 2).Name, "and so does each appearance of a verdict")
			}
		})
	}
}
