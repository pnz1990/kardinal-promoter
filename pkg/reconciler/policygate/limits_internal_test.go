// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expensiveExpr iterates over 1024 x 1024 pairs and concatenates a 1 KiB
// string in each step: about a million steps and a runtime cost of about a
// hundred million, a hundred times the cost limit.
var expensiveExpr = `random.seededString(1024, "a").split("").all(x, ` +
	`random.seededString(1024, "b").split("").all(y, x + "` + strings.Repeat("s", 1024) + `" != y))`

// TestEvaluate_Limits covers C04-gates-25: an expression cannot run without
// bound. The cost limit stops it even with no deadline, the deadline stops it
// even before the cost limit, and in both cases the gate fails closed.
func TestEvaluate_Limits(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name    string
		ctx     context.Context
		timeout time.Duration
		expr    string
		wantErr string
	}{
		{name: "cost limit", ctx: context.Background(), timeout: time.Hour, expr: expensiveExpr, wantErr: "cost limit exceeded"},
		{name: "deadline", ctx: cancelled, timeout: celEvalTimeout, expr: expensiveExpr, wantErr: "interrupted"},
		{name: "default limits", ctx: context.Background(), timeout: celEvalTimeout, expr: expensiveExpr, wantErr: "operation"},
		{name: "cheap expression", ctx: context.Background(), timeout: celEvalTimeout, expr: `[1, 2, 3].all(x, x > 0)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := newEvaluator()
			require.NoError(t, err)
			ev.timeout = tt.timeout
			pass, reason, err := ev.evaluate(tt.ctx, tt.expr, map[string]interface{}{})
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.True(t, pass)
				return
			}
			require.Error(t, err)
			assert.False(t, pass, "an expression stopped by a limit blocks the gate")
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, reason, "CEL evaluation error")
		})
	}
}

// TestEvaluate_JSONMarshalContext covers C04-gates-28: json.marshal of a
// context map built in Go (schedule.hour is an int) used to panic the
// reconciler. It now evaluates against the context buildContext produces.
func TestEvaluate_JSONMarshalContext(t *testing.T) {
	r, gate := docsCELFixture(t)
	celCtx, _, err := r.buildContext(t.Context(), gate, "app-v1")
	require.NoError(t, err)

	for _, expr := range []string{
		`json.marshal(schedule).contains("\"hour\":10")`,
		`json.marshal(bundle) != ""`,
		`json.marshal(upstream) != ""`,
		`json.unmarshal(json.marshal(schedule)).dayOfWeek == "Tuesday"`,
	} {
		t.Run(expr, func(t *testing.T) {
			var (
				pass   bool
				reason string
			)
			require.NotPanics(t, func() { pass, reason, err = r.eval.evaluate(t.Context(), expr, celCtx) })
			require.NoError(t, err, reason)
			assert.True(t, pass, reason)
		})
	}
}

// TestGetOrCompile_CacheBounded covers C04-gates-25: gate expressions are
// user-written, so the compiled-program cache must not grow with every distinct
// expression ever seen.
func TestGetOrCompile_CacheBounded(t *testing.T) {
	ev, err := newEvaluator()
	require.NoError(t, err)
	for i := 0; i < maxCachedPrograms+50; i++ {
		_, err := ev.getOrCompile(fmt.Sprintf("schedule.hour >= %d", i))
		require.NoError(t, err)
		require.LessOrEqual(t, len(ev.cache), maxCachedPrograms)
	}
	pass, _, err := ev.evaluate(t.Context(), "schedule.hour >= 3", map[string]interface{}{"schedule": map[string]interface{}{"hour": 10}})
	require.NoError(t, err)
	assert.True(t, pass, "an evicted expression is recompiled on demand")
}
