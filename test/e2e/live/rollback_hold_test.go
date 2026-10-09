//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestRollback_Hold checks kardinal rollback --hold on test -> prod. A
// gate on prod blocks every rollback. The held rollback passes it anyway,
// with an EXEMPT reason naming the hold, a GateEvaluated AuditEvent and a
// GateExempted Warning Event. The Pipeline records the hold, and explain
// shows it. A Bundle created after the rollback promotes to test but gets
// no prod step, and prod stays on the rollback, which is not superseded. A
// second hold of prod is refused. After kardinal release-hold, the newer
// Bundle promotes to prod.
//
// Covers RB-HOLD-01.
func TestRollback_Hold(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	const expr = `!("kardinal.io/rollback" in bundle.labels)`
	e.CreateGate(t, framework.Gate(a.ns, "no-rollbacks", "prod", expr, recheck))
	a.apply(t, a.pipeline(nil))
	ctx := context.Background()

	b1 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	rbVerified(t, a, b1, "test", "prod")
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	rbVerified(t, a, b2, "test", "prod")
	assertEnvAt(t, a, "prod", fixtures.V3)

	refused, err := e.Kardinal(t, a.ns, "rollback", pipelineName, "--env", "prod", "--hold")
	require.Error(t, err)
	assert.Contains(t, refused, "rollback --hold needs --reason")

	const reason = "INC-42: v3 leaks connections"
	out, rb := rbRollback(t, a, "prod", "--hold", "--reason", reason)
	assert.Contains(t, out, fmt.Sprintf("Environment prod held on %s: no other Bundle promotes there until: kardinal release-hold %s --env prod",
		rb, pipelineName))
	assert.Contains(t, out, "Gates that would block the rollback pass as EXEMPT (audited).")

	var p v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &p))
	require.Len(t, p.Spec.Holds, 1)
	hold := p.Spec.Holds[0]
	assert.Equal(t, "prod", hold.Environment)
	assert.Equal(t, rb, hold.Bundle)
	assert.Equal(t, reason, hold.Reason)
	assert.NotEmpty(t, hold.CreatedBy)
	require.NotNil(t, hold.CreatedAt)

	// The gate that blocks every rollback passes the held one, never silently.
	exempt := fmt.Sprintf("EXEMPT: rollback %s holds prod (by %s: %s); without the hold: ", rb, hold.CreatedBy, reason)
	gate := e.WaitGateReady(t, a.ns, rb, "prod", "no-rollbacks", true, exempt, gateTimeout)
	assert.Contains(t, gate.Status.Reason, expr+" = false", "the gate's own result stays visible")
	rbVerified(t, a, rb, "test", "prod")
	assertEnvAt(t, a, "prod", fixtures.V2)
	framework.Eventually(t, time.Minute, "a GateEvaluated AuditEvent of the exemption", func(ctx context.Context) (bool, string) {
		evs, err := e.AuditEvents(ctx, a.ns, rb)
		if err != nil {
			return false, err.Error()
		}
		var seen []string
		for _, ae := range evs {
			if ae.Spec.Action == "GateEvaluated" && strings.Contains(ae.Spec.Message, "EXEMPT: rollback "+rb) {
				return ae.Spec.Outcome == "Success", "outcome " + ae.Spec.Outcome
			}
			seen = append(seen, ae.Spec.Action+": "+ae.Spec.Message)
		}
		return false, strings.Join(seen, "; ")
	})
	framework.Eventually(t, time.Minute, "a GateExempted Warning Event on the gate", func(ctx context.Context) (bool, string) {
		evs, err := e.Events(ctx, a.ns, "PolicyGate", gate.Name)
		if err != nil {
			return false, err.Error()
		}
		for _, ev := range rbReason(evs, "GateExempted") {
			return ev.Type == "Warning" && strings.Contains(ev.Note, exempt), ev.Type + " " + ev.Note
		}
		return false, fmt.Sprintf("%d events, none GateExempted", len(evs))
	})
	explain := e.MustKardinal(t, a.ns, "explain", pipelineName, "--env", "prod")
	assert.Contains(t, explain, fmt.Sprintf("prod: held on rollback %s by %s (%s)", rb, hold.CreatedBy, reason))
	assert.Contains(t, explain, "Release with: kardinal release-hold "+pipelineName+" --env prod")

	// A newer Bundle promotes to test and then waits: no prod step while
	// prod is held, and the rollback is not superseded.
	b3 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitStepState(t, a.ns, pipelineName, b3, "test", "Verified", promoteTimeout)
	e.NoStep(t, a.ns, pipelineName, b3, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V2)
	assert.Equal(t, "Verified", getBundle(t, e, a.ns, rb).Status.Phase, "the held rollback is not superseded")
	again := rbRefused(t, a, "--env", "prod", "--hold", "--reason", "again")
	assert.Contains(t, again, "environment prod is already held on "+rb)

	rel := e.MustKardinal(t, a.ns, "release-hold", pipelineName, "--env", "prod")
	assert.Equal(t, fmt.Sprintf("Released the hold of %s on prod (rollback %s, held by %s: %s).\n",
		pipelineName, rb, hold.CreatedBy, reason), rel)
	rbVerified(t, a, b3, "prod")
	assertEnvAt(t, a, "prod", fixtures.V3)
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &p))
	assert.Empty(t, p.Spec.Holds)
	_, err = e.Kardinal(t, a.ns, "release-hold", pipelineName, "--env", "prod")
	assert.Error(t, err, "nothing is held any more")
}
