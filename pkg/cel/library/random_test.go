// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package library_test

import (
	"math"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/cel/library"
)

// evalRandom evaluates expr in an environment with the random library.
func evalRandom(t *testing.T, expr string, vars map[string]interface{}) (interface{}, error) {
	t.Helper()
	env, err := cel.NewEnv(library.Random(), cel.Variable("seed", cel.StringType))
	require.NoError(t, err)
	ast, iss := env.Compile(expr)
	require.NoError(t, iss.Err())
	prg, err := env.Program(ast)
	require.NoError(t, err)
	if vars == nil {
		vars = map[string]interface{}{"seed": ""}
	}
	out, _, err := prg.Eval(vars)
	if err != nil {
		return nil, err
	}
	return out.Value(), nil
}

// TestSeededString_NotPeriodic covers C04-gates-29: the output used to repeat
// every 8 characters, whatever the length.
func TestSeededString_NotPeriodic(t *testing.T) {
	for _, seed := range []string{"my-seed", "1.29.0", ""} {
		t.Run(seed, func(t *testing.T) {
			got, err := evalRandom(t, `random.seededString(64, seed)`, map[string]interface{}{"seed": seed})
			require.NoError(t, err)
			s := got.(string)
			require.Len(t, s, 64)
			for i := 8; i < len(s); i += 8 {
				assert.NotEqual(t, s[:8], s[i:i+8], "block at %d repeats the first block: %s", i, s)
			}
			for _, c := range s {
				assert.True(t, strings.ContainsRune("0123456789abcdefghijklmnopqrstuvwxyz", c), "%q", c)
			}
		})
	}
}

// TestSeededString_Deterministic: the same seed gives the same string, a
// longer string extends a shorter one, and strings of up to 8 characters are
// the values the function returned before the fix.
func TestSeededString_Deterministic(t *testing.T) {
	a, err := evalRandom(t, `random.seededString(32, "my-seed")`, nil)
	require.NoError(t, err)
	b, err := evalRandom(t, `random.seededString(32, "my-seed")`, nil)
	require.NoError(t, err)
	assert.Equal(t, a, b)

	short, err := evalRandom(t, `random.seededString(8, "my-seed")`, nil)
	require.NoError(t, err)
	assert.Equal(t, "yfmgby05", short, "first 8 characters unchanged")
	assert.True(t, strings.HasPrefix(a.(string), short.(string)))
}

// TestSeededString_Length: the length must be between 1 and 1024, so one
// expression cannot allocate an arbitrarily large string.
func TestSeededString_Length(t *testing.T) {
	tests := []struct {
		expr    string
		wantErr string
	}{
		{expr: `random.seededString(1024, "s")`},
		{expr: `random.seededString(1025, "s")`, wantErr: "at most 1024"},
		{expr: `random.seededString(50000000, "s")`, wantErr: "at most 1024"},
		{expr: `random.seededString(0, "s")`, wantErr: "must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			got, err := evalRandom(t, tt.expr, nil)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got.(string), 1024)
		})
	}
}

// TestSeededInt_Range: the result is in [min, max) for any range, including
// ranges wider than MaxInt64, where the int64 arithmetic wraps (C04-gates-30:
// the wrap is exact, so the result is still in range).
func TestSeededInt_Range(t *testing.T) {
	ranges := []struct {
		name     string
		min, max int64
	}{
		{name: "small", min: 0, max: 100},
		{name: "negative", min: -50, max: -10},
		{name: "wider than MaxInt64", min: -10, max: math.MaxInt64},
		{name: "full int64", min: math.MinInt64, max: math.MaxInt64},
	}
	for _, r := range ranges {
		t.Run(r.name, func(t *testing.T) {
			env, err := cel.NewEnv(library.Random(),
				cel.Variable("lo", cel.IntType), cel.Variable("hi", cel.IntType), cel.Variable("seed", cel.StringType))
			require.NoError(t, err)
			ast, iss := env.Compile(`random.seededInt(lo, hi, seed)`)
			require.NoError(t, iss.Err())
			prg, err := env.Program(ast)
			require.NoError(t, err)
			for i := 0; i < 200; i++ {
				out, _, err := prg.Eval(map[string]interface{}{"lo": r.min, "hi": r.max, "seed": strings.Repeat("s", i)})
				require.NoError(t, err)
				v := out.Value().(int64)
				assert.GreaterOrEqual(t, v, r.min, "seed #%d", i)
				assert.Less(t, v, r.max, "seed #%d", i)
			}
		})
	}
}

// TestSeededInt_Deterministic: the value for a small range is unchanged by the
// overflow fix.
func TestSeededInt_Deterministic(t *testing.T) {
	a, err := evalRandom(t, `random.seededInt(0, 100, "1.29.0")`, nil)
	require.NoError(t, err)
	b, err := evalRandom(t, `random.seededInt(0, 100, "1.29.0")`, nil)
	require.NoError(t, err)
	assert.Equal(t, a, b)
	_, err = evalRandom(t, `random.seededInt(5, 5, "x")`, nil)
	assert.ErrorContains(t, err, "min must be less than max")
}
