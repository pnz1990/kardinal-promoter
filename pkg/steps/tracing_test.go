// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestEngine_StepSpans: with tracing on, every step ExecuteFrom runs is a
// span "step <name>" with the step's index, environment and status, and a
// failed step's span is marked failed. Not parallel: the provider is global.
func TestEngine_StepSpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	ok := &countingStep{name: "test-span-ok", statuses: []steps.StepStatus{steps.StepSuccess}}
	bad := &countingStep{name: "test-span-bad", statuses: []steps.StepStatus{steps.StepFailed}}
	steps.Register(ok)
	steps.Register(bad)
	eng := steps.NewEngine([]string{ok.name, bad.name})
	state := &steps.StepState{Environment: v1alpha1.EnvironmentSpec{Name: "prod"}}
	_, _, err := eng.ExecuteFrom(context.Background(), state, 0)
	require.Error(t, err)

	spans := rec.Ended()
	require.Len(t, spans, 2)
	for i, want := range []struct {
		name, status string
		code         codes.Code
	}{{"step test-span-ok", "Success", codes.Unset}, {"step test-span-bad", "Failed", codes.Error}} {
		s := spans[i]
		assert.Equal(t, want.name, s.Name())
		got := map[string]string{}
		for _, kv := range s.Attributes() {
			got[string(kv.Key)] = kv.Value.String()
		}
		assert.Equal(t, "prod", got["kardinal.environment"])
		assert.Equal(t, want.status, got["kardinal.step.status"])
		assert.Equal(t, want.code, s.Status().Code)
	}
}
