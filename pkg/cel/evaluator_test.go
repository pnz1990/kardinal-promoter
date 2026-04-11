// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cel_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	celpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/cel"
)

// buildScheduleCtx builds a CEL context for schedule testing with the given time.
func buildScheduleCtx(t time.Time) map[string]interface{} {
	return map[string]interface{}{
		"schedule": map[string]interface{}{
			"isWeekend": t.Weekday() == time.Saturday || t.Weekday() == time.Sunday,
			"hour":      t.Hour(),
			"dayOfWeek": t.Weekday().String(),
		},
		"bundle": map[string]interface{}{
			"type":    "image",
			"version": "v1.0.0",
			"provenance": map[string]interface{}{
				"author":    "alice",
				"commitSHA": "abc123",
				"ciRunURL":  "https://ci.example.com/run/1",
			},
			"intent": map[string]interface{}{
				"targetEnvironment": "prod",
			},
		},
		"environment": map[string]interface{}{
			"name": "prod",
		},
	}
}

// TestEvaluator_WeekendGate_Weekday verifies that !schedule.isWeekend passes on a weekday.
func TestEvaluator_WeekendGate_Weekday(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	// Tuesday
	tuesday := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	ctx := buildScheduleCtx(tuesday)

	pass, reason, err := eval.Evaluate("!schedule.isWeekend", ctx)
	require.NoError(t, err)
	assert.True(t, pass, "!schedule.isWeekend must pass on Tuesday")
	assert.NotEmpty(t, reason)
}

// TestEvaluator_WeekendGate_Weekend verifies that !schedule.isWeekend fails on a weekend.
func TestEvaluator_WeekendGate_Weekend(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	// Saturday
	saturday := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	ctx := buildScheduleCtx(saturday)

	pass, reason, err := eval.Evaluate("!schedule.isWeekend", ctx)
	require.NoError(t, err)
	assert.False(t, pass, "!schedule.isWeekend must fail on Saturday")
	assert.NotEmpty(t, reason)
}

// TestEvaluator_AuthorGate_Human verifies author gate passes for a human author.
func TestEvaluator_AuthorGate_Human(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildScheduleCtx(time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC))
	ctx["bundle"].(map[string]interface{})["provenance"] = map[string]interface{}{
		"author": "alice",
	}

	pass, _, err := eval.Evaluate(`bundle.provenance.author != "dependabot[bot]"`, ctx)
	require.NoError(t, err)
	assert.True(t, pass)
}

// TestEvaluator_AuthorGate_Bot verifies author gate fails for dependabot.
func TestEvaluator_AuthorGate_Bot(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildScheduleCtx(time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC))
	ctx["bundle"].(map[string]interface{})["provenance"] = map[string]interface{}{
		"author": "dependabot[bot]",
	}

	pass, _, err := eval.Evaluate(`bundle.provenance.author != "dependabot[bot]"`, ctx)
	require.NoError(t, err)
	assert.False(t, pass)
}

// TestEvaluator_SyntaxError verifies fail-closed on CEL syntax error.
func TestEvaluator_SyntaxError(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildScheduleCtx(time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC))
	pass, reason, err := eval.Evaluate("!! invalid CEL {{{", ctx)
	require.Error(t, err, "syntax error must return an error")
	assert.False(t, pass, "syntax error must fail-closed")
	assert.NotEmpty(t, reason)
}

// TestEvaluator_NonBooleanExpression verifies fail-closed for non-boolean result.
func TestEvaluator_NonBooleanExpression(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildScheduleCtx(time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC))
	// bundle.version is a string, not a boolean
	pass, reason, err := eval.Evaluate("bundle.version", ctx)
	require.Error(t, err, "non-boolean expression must return an error")
	assert.False(t, pass, "non-boolean expression must fail-closed")
	assert.NotEmpty(t, reason)
}

// TestEvaluator_ConfigBundleType verifies bundle.type == "config" is evaluatable.
func TestEvaluator_ConfigBundleType(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildScheduleCtx(time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC))
	ctx["bundle"].(map[string]interface{})["type"] = "config"

	pass, _, err := eval.Evaluate(`bundle.type == "config"`, ctx)
	require.NoError(t, err)
	assert.True(t, pass)
}

// TestEvaluator_CacheHit verifies that the second evaluation of the same expression
// uses the cached program (same result, no error).
func TestEvaluator_CacheHit(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildScheduleCtx(time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC))

	pass1, _, err1 := eval.Evaluate("!schedule.isWeekend", ctx)
	pass2, _, err2 := eval.Evaluate("!schedule.isWeekend", ctx)
	require.NoError(t, err1)
	require.NoError(t, err2)
	assert.Equal(t, pass1, pass2)
}

// TestEvaluator_Benchmark verifies that CEL evaluation completes under 10ms p99.
// This is a smoke test (not a true p99 benchmark) — just ensures no gross slowness.
func TestEvaluator_Benchmark(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildScheduleCtx(time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC))
	expr := `!schedule.isWeekend && bundle.type == "image" && bundle.provenance.author != "dependabot[bot]"`

	// Warmup
	_, _, _ = eval.Evaluate(expr, ctx)

	// 100 iterations to get a sense of timing
	start := time.Now()
	for i := 0; i < 100; i++ {
		_, _, err := eval.Evaluate(expr, ctx)
		require.NoError(t, err)
	}
	elapsed := time.Since(start)
	avgNs := elapsed.Nanoseconds() / 100
	assert.Less(t, avgNs, int64(10*time.Millisecond),
		"average CEL evaluation must be under 10ms, got %v", time.Duration(avgNs))
}

// buildMetricsCtx builds a CEL context that includes the metrics and upstream variables.
func buildMetricsCtx(t time.Time) map[string]interface{} {
	base := buildScheduleCtx(t)
	base["metrics"] = map[string]interface{}{
		"error_rate": map[string]interface{}{
			"value":  "0.005",
			"result": "Pass",
		},
	}
	base["upstream"] = map[string]interface{}{
		"uat": map[string]interface{}{
			"soakMinutes": int64(45),
		},
	}
	return base
}

// TestEvaluator_MetricsContext_PassWhenResultPass verifies metrics["name"].result == "Pass" works.
func TestEvaluator_MetricsContext_PassWhenResultPass(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildMetricsCtx(time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC))
	pass, _, err := eval.Evaluate(`metrics["error_rate"].result == "Pass"`, ctx)
	require.NoError(t, err)
	assert.True(t, pass)
}

// TestEvaluator_MetricsContext_BlockWhenResultFail verifies metrics["name"].result == "Fail" blocks.
func TestEvaluator_MetricsContext_BlockWhenResultFail(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildMetricsCtx(time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC))
	// Override to Fail
	ctx["metrics"] = map[string]interface{}{
		"error_rate": map[string]interface{}{
			"value":  "0.05",
			"result": "Fail",
		},
	}
	pass, _, err := eval.Evaluate(`metrics["error_rate"].result == "Pass"`, ctx)
	require.NoError(t, err)
	assert.False(t, pass)
}

// TestEvaluator_UpstreamContext_SoakMinutesPass verifies upstream["uat"].soakMinutes >= 30 passes.
func TestEvaluator_UpstreamContext_SoakMinutesPass(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildMetricsCtx(time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC))
	// 45 minutes soak
	pass, _, err := eval.Evaluate(`upstream["uat"].soakMinutes >= 30`, ctx)
	require.NoError(t, err)
	assert.True(t, pass)
}

// TestEvaluator_UpstreamContext_SoakMinutesBlock verifies upstream["uat"].soakMinutes >= 30 blocks when < 30.
func TestEvaluator_UpstreamContext_SoakMinutesBlock(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildMetricsCtx(time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC))
	ctx["upstream"] = map[string]interface{}{
		"uat": map[string]interface{}{
			"soakMinutes": int64(10),
		},
	}
	pass, _, err := eval.Evaluate(`upstream["uat"].soakMinutes >= 30`, ctx)
	require.NoError(t, err)
	assert.False(t, pass)
}
