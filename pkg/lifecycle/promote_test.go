// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func TestPlanPromote(t *testing.T) {
	fanIn := pipeline("app", "eu", "us", "prod")
	fanIn.Spec.Environments[2].DependsOn = []string{"eu", "us"}

	tests := []struct {
		name     string
		pipeline *v1alpha1.Pipeline
		objs     []client.Object
		env      string
		wantSrc  string
		wantTag  string
		wantErr  error
	}{
		{
			name: "copies the artifacts of the newest bundle verified upstream",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10), bundle("v3", "app", "3", 20),
				step("v1", "app", "uat", "Verified", 1), step("v1", "app", "prod", "Verified", 2),
				step("v2", "app", "uat", "Verified", 11),
				step("v3", "app", "uat", "Promoting", 21),
			},
			env: "prod", wantSrc: "v2", wantTag: "2",
		},
		{
			name: "a failed attempt in the target environment can be promoted again",
			objs: []client.Object{
				bundle("v1", "app", "1", 0),
				step("v1", "app", "uat", "Verified", 1), step("v1", "app", "prod", "Failed", 2),
			},
			env: "prod", wantSrc: "v1", wantTag: "1",
		},
		{
			name: "a bundle without artifacts is never the source",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("empty", "app", "", 10),
				step("v1", "app", "uat", "Verified", 1), step("empty", "app", "uat", "Verified", 11),
			},
			env: "prod", wantSrc: "v1", wantTag: "1",
		},
		{
			name:     "fan-in needs every upstream verified",
			pipeline: fanIn,
			objs: []client.Object{
				bundle("v1", "app", "1", 0), bundle("v2", "app", "2", 10),
				step("v1", "app", "eu", "Verified", 1), step("v1", "app", "us", "Verified", 1),
				step("v2", "app", "eu", "Verified", 11), step("v2", "app", "us", "HealthChecking", 11),
			},
			env: "prod", wantSrc: "v1", wantTag: "1",
		},
		{
			name:    "the first environment has nothing upstream",
			objs:    []client.Object{bundle("v1", "app", "1", 0)},
			env:     "test",
			wantErr: lifecycle.ErrInvalid,
		},
		{
			name:    "nothing verified upstream",
			objs:    []client.Object{bundle("v1", "app", "1", 0), step("v1", "app", "uat", "Promoting", 1)},
			env:     "prod",
			wantErr: lifecycle.ErrConflict,
		},
		{
			name: "the newest verified bundle is already in the environment",
			objs: []client.Object{
				bundle("v1", "app", "1", 0),
				step("v1", "app", "uat", "Verified", 1), step("v1", "app", "prod", "Verified", 2),
			},
			env:     "prod",
			wantErr: lifecycle.ErrConflict,
		},
		{
			name: "the newest verified bundle is already being promoted there",
			objs: []client.Object{
				bundle("v1", "app", "1", 0),
				step("v1", "app", "uat", "Verified", 1), step("v1", "app", "prod", "WaitingForMerge", 2),
			},
			env:     "prod",
			wantErr: lifecycle.ErrConflict,
		},
		{
			name: "a newer bundle still promoting would be superseded",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), phase(bundle("v2", "app", "2", 10), "Promoting"),
				step("v1", "app", "uat", "Verified", 1), step("v2", "app", "test", "Verified", 11),
			},
			env:     "prod",
			wantErr: lifecycle.ErrConflict,
		},
		{
			name: "the source still promoting reaches the environment on its own",
			objs: []client.Object{
				phase(bundle("v1", "app", "1", 0), "Promoting"),
				step("v1", "app", "uat", "Verified", 1),
			},
			env:     "prod",
			wantErr: lifecycle.ErrConflict,
		},
		{
			name: "a finished or failed newer bundle is not in the way",
			objs: []client.Object{
				bundle("v1", "app", "1", 0), phase(bundle("v2", "app", "2", 10), "Failed"),
				step("v1", "app", "uat", "Verified", 1), step("v2", "app", "test", "Failed", 11),
			},
			env: "prod", wantSrc: "v1", wantTag: "1",
		},
		{
			name:    "unknown environment",
			env:     "staging",
			wantErr: lifecycle.ErrInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.pipeline
			if p == nil {
				p = pipeline("app", "test", "uat", "prod")
			}
			c := newClient(t, append(tc.objs, p)...)
			plan, err := lifecycle.PlanPromote(context.Background(), c, lifecycle.PromoteRequest{
				Namespace: ns, Pipeline: "app", Environment: tc.env, Actor: "alice", Now: t0,
			})
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			b := plan.Bundle
			assert.Equal(t, tc.wantSrc, plan.Source.Name)
			require.Len(t, b.Spec.Images, 1, "a promote bundle always carries images")
			assert.Equal(t, tc.wantTag, b.Spec.Images[0].Tag)
			assert.Equal(t, "image", b.Spec.Type)
			assert.Equal(t, tc.env, b.Spec.Intent.TargetEnvironment)
			assert.Equal(t, tc.wantSrc, b.Annotations[lifecycle.AnnotationPromotedFrom])
			assert.Equal(t, "alice", b.Annotations[lifecycle.AnnotationRequestedBy])
			assert.Equal(t, "sha-"+tc.wantTag, b.Spec.Provenance.CommitSHA)
			assert.Empty(t, b.Spec.Provenance.RollbackOf)
			assert.Equal(t, "app", b.Labels[lifecycle.LabelPipeline])
		})
	}

	t.Run("unknown pipeline", func(t *testing.T) {
		_, err := lifecycle.PlanPromote(context.Background(), newClient(t), lifecycle.PromoteRequest{
			Namespace: ns, Pipeline: "nope", Environment: "prod",
		})
		require.ErrorIs(t, err, lifecycle.ErrNotFound)
	})
}
