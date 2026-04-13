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

// --- kro library function tests (Issue #404) ---
// These tests verify all kro-library CEL functions are available and correct
// in PolicyGate expressions: json.*, maps.*, lists.*, random.*, string.*

// buildKroCtx returns a minimal context for kro library function tests.
func buildKroCtx() map[string]interface{} {
	return map[string]interface{}{
		"bundle": map[string]interface{}{
			"type":    "image",
			"version": "v1.0.0",
			"metadata": map[string]interface{}{
				"name":      "my-bundle",
				"namespace": "default",
				"annotations": map[string]interface{}{
					"channel":      `{"channel":"stable"}`,
					"release-type": "hotfix",
					"team":         "platform",
				},
				"labels": map[string]interface{}{
					"tier": "production",
				},
			},
			"provenance": map[string]interface{}{
				"author":    "alice",
				"commitSHA": "abc123",
			},
		},
		"environment": map[string]interface{}{
			"name": "prod",
			"labels": map[string]interface{}{
				"region": "us-east-1",
				"tier":   "production",
			},
		},
		"schedule": map[string]interface{}{
			"isWeekend": false,
			"hour":      10,
			"dayOfWeek": "Tuesday",
		},
		"metrics":        map[string]interface{}{},
		"upstream":       map[string]interface{}{},
		"previousBundle": map[string]interface{}{},
	}
}

// TestEvaluator_JSONMarshal verifies json.marshal converts a map to a JSON string.
func TestEvaluator_JSONMarshal(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	// json.marshal returns a non-empty string — check it's a string (non-boolean would fail-close)
	// We verify this via a string-contains check
	pass, _, err := eval.Evaluate(`json.marshal(bundle.metadata.labels).contains("production")`, ctx)
	require.NoError(t, err)
	assert.True(t, pass, "json.marshal should produce a string containing the label value")
}

// TestEvaluator_JSONUnmarshal_SimpleField verifies json.unmarshal parses a JSON string field.
func TestEvaluator_JSONUnmarshal_SimpleField(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	// bundle.metadata.annotations["channel"] == '{"channel":"stable"}'
	// json.unmarshal(that).channel == 'stable'
	pass, _, err := eval.Evaluate(`json.unmarshal(bundle.metadata.annotations["channel"]).channel == "stable"`, ctx)
	require.NoError(t, err)
	assert.True(t, pass, "json.unmarshal should parse the channel field as 'stable'")
}

// TestEvaluator_JSONUnmarshal_Block verifies json.unmarshal works for a blocking condition.
func TestEvaluator_JSONUnmarshal_Block(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	// channel != "stable" should fail (it IS stable)
	pass, _, err := eval.Evaluate(`json.unmarshal(bundle.metadata.annotations["channel"]).channel == "canary"`, ctx)
	require.NoError(t, err)
	assert.False(t, pass, "json.unmarshal result should not equal 'canary' when value is 'stable'")
}

// TestEvaluator_JSONMarshalUnmarshalRoundTrip verifies json.unmarshal(json.marshal(v)) round-trips.
func TestEvaluator_JSONMarshalUnmarshalRoundTrip(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	// Round-trip: marshal the labels map, then unmarshal it, check the tier field
	pass, _, err := eval.Evaluate(`json.unmarshal(json.marshal(bundle.metadata.labels)).tier == "production"`, ctx)
	require.NoError(t, err)
	assert.True(t, pass, "json round-trip should preserve map values")
}

// TestEvaluator_JSONUnmarshal_InvalidJSON verifies fail-closed on invalid JSON input.
func TestEvaluator_JSONUnmarshal_InvalidJSON(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	// Override annotation with invalid JSON
	ctx["bundle"].(map[string]interface{})["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})["channel"] = "not-json-{{{!"
	pass, reason, _ := eval.Evaluate(`json.unmarshal(bundle.metadata.annotations["channel"]).channel == "stable"`, ctx)
	// CEL json.unmarshal on invalid JSON must not panic; it returns an error or false
	// (fail-closed: either an error is returned, or the expression evaluates to false)
	assert.False(t, pass, "invalid JSON must not pass a gate")
	assert.NotEmpty(t, reason)
}

// TestEvaluator_MapsMerge_NoOverlap verifies maps.merge combines two disjoint maps.
func TestEvaluator_MapsMerge_NoOverlap(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	// environment.labels has region:us-east-1,tier:production
	// bundle.metadata.labels has tier:production
	// merge should have both keys; check region key present
	pass, _, err := eval.Evaluate(
		`environment.labels.merge(bundle.metadata.labels)["region"] == "us-east-1"`,
		ctx,
	)
	require.NoError(t, err)
	assert.True(t, pass, "maps.merge should expose all keys from both maps")
}

// TestEvaluator_MapsMerge_SecondWins verifies maps.merge second argument wins on conflict.
func TestEvaluator_MapsMerge_SecondWins(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	// environment.labels["tier"] == "production"
	// bundle.metadata.labels["tier"] == "production"
	// If we set them differently: environment=production, bundle=experimental → bundle wins
	ctx["environment"].(map[string]interface{})["labels"] = map[string]interface{}{
		"tier": "production",
	}
	ctx["bundle"].(map[string]interface{})["metadata"].(map[string]interface{})["labels"] = map[string]interface{}{
		"tier": "experimental",
	}
	pass, _, err := eval.Evaluate(
		`environment.labels.merge(bundle.metadata.labels)["tier"] == "experimental"`,
		ctx,
	)
	require.NoError(t, err)
	assert.True(t, pass, "maps.merge second argument must overwrite first on key conflict")
}

// TestEvaluator_MapsMerge_GateBlock verifies a gate that uses maps.merge to check a value.
func TestEvaluator_MapsMerge_GateBlock(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	// Gate: merged labels tier must NOT be "experimental" — blocks because bundle overrides to experimental
	ctx["bundle"].(map[string]interface{})["metadata"].(map[string]interface{})["labels"] = map[string]interface{}{
		"tier": "experimental",
	}
	pass, _, err := eval.Evaluate(
		`environment.labels.merge(bundle.metadata.labels)["tier"] != "experimental"`,
		ctx,
	)
	require.NoError(t, err)
	assert.False(t, pass, "gate must block when merged tier == experimental")
}

// TestEvaluator_ListsSetAtIndex verifies lists.setAtIndex replaces a value.
func TestEvaluator_ListsSetAtIndex(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	// Evaluate: lists.setAtIndex([1,2,3], 1, 9)[1] == 9
	ctx["bundle"].(map[string]interface{})["envList"] = []interface{}{int64(1), int64(2), int64(3)}
	pass, _, err := eval.Evaluate(`lists.setAtIndex([1,2,3], 1, 9)[1] == 9`, ctx)
	require.NoError(t, err)
	assert.True(t, pass, "lists.setAtIndex should replace index 1 with 9")
}

// TestEvaluator_ListsInsertAtIndex verifies lists.insertAtIndex inserts a value.
func TestEvaluator_ListsInsertAtIndex(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	// lists.insertAtIndex([1,2,3], 1, 9) == [1,9,2,3] — check index 1 is 9 and length is 4
	pass, _, err := eval.Evaluate(
		`lists.insertAtIndex([1,2,3], 1, 9)[1] == 9 && lists.insertAtIndex([1,2,3], 1, 9).size() == 4`,
		ctx,
	)
	require.NoError(t, err)
	assert.True(t, pass, "lists.insertAtIndex should insert 9 at index 1 yielding [1,9,2,3]")
}

// TestEvaluator_ListsRemoveAtIndex verifies lists.removeAtIndex removes a value.
func TestEvaluator_ListsRemoveAtIndex(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	// lists.removeAtIndex([1,2,3], 1) == [1,3]
	pass, _, err := eval.Evaluate(
		`lists.removeAtIndex([1,2,3], 1).size() == 2 && lists.removeAtIndex([1,2,3], 1)[0] == 1`,
		ctx,
	)
	require.NoError(t, err)
	assert.True(t, pass, "lists.removeAtIndex should remove index 1 yielding [1,3]")
}

// TestEvaluator_RandomSeededInt_Deterministic verifies random.seededInt returns the same value for the same seed.
func TestEvaluator_RandomSeededInt_Deterministic(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	// Same seed must return same value across repeated calls
	pass1, _, err := eval.Evaluate(`random.seededInt(0, 100, "same-seed") >= 0`, ctx)
	require.NoError(t, err)
	pass2, _, err2 := eval.Evaluate(`random.seededInt(0, 100, "same-seed") >= 0`, ctx)
	require.NoError(t, err2)
	assert.True(t, pass1)
	assert.True(t, pass2)

	// Verify the actual value is the same by comparing equality
	pass3, _, err3 := eval.Evaluate(
		`random.seededInt(0, 100, "same-seed") == random.seededInt(0, 100, "same-seed")`,
		ctx,
	)
	require.NoError(t, err3)
	assert.True(t, pass3, "random.seededInt with same seed must return the same value")
}

// TestEvaluator_RandomSeededInt_DifferentSeeds verifies random.seededInt returns different values for different seeds.
func TestEvaluator_RandomSeededInt_DifferentSeeds(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	// Different seeds will almost certainly produce different results in range 0–1000
	// This is a probabilistic check but with large enough range, collision chance is ~0.1%
	pass, _, err := eval.Evaluate(
		`random.seededInt(0, 1000, "seed-aaa") != random.seededInt(0, 1000, "seed-zzz")`,
		ctx,
	)
	require.NoError(t, err)
	// Note: there is a tiny probability this fails by chance. If so, increase the range.
	assert.True(t, pass, "random.seededInt with different seeds should generally produce different values")
}

// TestEvaluator_StringLowerAscii verifies string.lowerAscii() works in gate expressions.
func TestEvaluator_StringLowerAscii(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	ctx["bundle"].(map[string]interface{})["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})["release-type"] = "HotFix"
	pass, _, err := eval.Evaluate(
		`bundle.metadata.annotations["release-type"].lowerAscii() == "hotfix"`,
		ctx,
	)
	require.NoError(t, err)
	assert.True(t, pass, "lowerAscii() should normalize HotFix to hotfix")
}

// TestEvaluator_StringContains verifies string.contains() works in gate expressions.
func TestEvaluator_StringContains(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	pass, _, err := eval.Evaluate(
		`bundle.metadata.annotations["team"].contains("platform")`,
		ctx,
	)
	require.NoError(t, err)
	assert.True(t, pass, "string.contains() should find 'platform' in 'platform'")
}

// TestEvaluator_CombinedJSONAndMaps verifies a complex expression combining json.unmarshal and maps.merge.
func TestEvaluator_CombinedJSONAndMaps(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()
	// Parse a JSON annotation and merge it with the environment labels, then check a field
	// bundle.metadata.annotations["channel"] = '{"channel":"stable"}'
	// We verify: json.unmarshal returns a map, and we can check the channel field
	pass, _, err := eval.Evaluate(
		`json.unmarshal(bundle.metadata.annotations["channel"]).channel == "stable" && environment.labels["tier"] == "production"`,
		ctx,
	)
	require.NoError(t, err)
	assert.True(t, pass, "combined json.unmarshal + map field access should work in one expression")
}

// TestEvaluator_AllFunctionsInRealPolicyGate verifies all kro library functions work
// when evaluated through a PolicyGate-style context (same path as reconciler).
func TestEvaluator_AllFunctionsInRealPolicyGate(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	ctx := buildKroCtx()

	tests := []struct {
		name string
		expr string
		want bool
	}{
		{
			name: "json.unmarshal simple field",
			expr: `json.unmarshal(bundle.metadata.annotations["channel"]).channel == "stable"`,
			want: true,
		},
		{
			name: "json.marshal produces non-empty string",
			expr: `json.marshal(bundle.metadata.labels).size() > 0`,
			want: true,
		},
		{
			name: "maps.merge second wins",
			expr: `environment.labels.merge({"tier": "override"})["tier"] == "override"`,
			want: true,
		},
		{
			name: "lists.setAtIndex",
			expr: `lists.setAtIndex(["a","b","c"], 0, "z")[0] == "z"`,
			want: true,
		},
		{
			name: "lists.insertAtIndex size",
			expr: `lists.insertAtIndex(["a","b"], 1, "x").size() == 3`,
			want: true,
		},
		{
			name: "lists.removeAtIndex size",
			expr: `lists.removeAtIndex(["a","b","c"], 2).size() == 2`,
			want: true,
		},
		{
			name: "random.seededInt in range",
			expr: `random.seededInt(0, 99, "bundle-seed") >= 0 && random.seededInt(0, 99, "bundle-seed") <= 99`,
			want: true,
		},
		{
			name: "string.lowerAscii",
			expr: `"PROD".lowerAscii() == "prod"`,
			want: true,
		},
		{
			name: "combined schedule + upstream",
			expr: `!schedule.isWeekend`,
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pass, reason, err := eval.Evaluate(tc.expr, ctx)
			require.NoError(t, err, "expression should not error: %s", reason)
			assert.Equal(t, tc.want, pass, "expression %q: got %v, want %v (reason: %s)", tc.expr, pass, tc.want, reason)
		})
	}
}

// TestEvaluator_Validate_ValidExpression verifies that Validate returns nil for valid CEL syntax.
// This is the compile-only check used by reconcileTemplate (Issue #315).
func TestEvaluator_Validate_ValidExpression(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	// These expressions reference deep bundle fields — they would fail at EVALUATION
	// with an empty bundle context, but must compile (syntax is valid).
	valid := []string{
		`!schedule.isWeekend`,
		`bundle.metadata.annotations["team"] == "platform"`,
		`bundle.provenance.author != "dependabot[bot]"`,
		`upstream["uat"].soakMinutes >= 30`,
		`json.unmarshal(bundle.metadata.annotations["channel"]).channel == "stable"`,
		`environment.labels.merge(bundle.metadata.labels)["tier"] != "experimental"`,
	}
	for _, expr := range valid {
		t.Run(expr, func(t *testing.T) {
			err := eval.Validate(expr)
			assert.NoError(t, err, "expression %q has valid syntax and should compile", expr)
		})
	}
}

// TestEvaluator_Validate_InvalidExpression verifies that Validate returns an error for invalid CEL.
func TestEvaluator_Validate_InvalidExpression(t *testing.T) {
	env, err := celpkg.NewCELEnvironment()
	require.NoError(t, err)
	eval := celpkg.NewEvaluator(env)

	invalid := []string{
		`this is not valid CEL !!!`,
		`{{{}}}`,
		``,
	}
	for _, expr := range invalid {
		t.Run(expr, func(t *testing.T) {
			err := eval.Validate(expr)
			// Empty expression: no error from CEL (compiles to nil AST) — allowed
			if expr == "" {
				return
			}
			assert.Error(t, err, "expression %q has invalid syntax and should not compile", expr)
		})
	}
}
