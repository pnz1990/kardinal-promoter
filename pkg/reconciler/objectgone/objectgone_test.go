// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package objectgone_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
)

var pipelines = schema.GroupResource{Group: "kardinal.io", Resource: "pipelines"}

func TestIs(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "its NotFound", err: apierrors.NewNotFound(pipelines, "podinfo"), want: true},
		{name: "its NotFound, wrapped", want: true,
			err: fmt.Errorf("patch pipeline status: %w", apierrors.NewNotFound(pipelines, "podinfo"))},
		{name: "another object of the resource", err: apierrors.NewNotFound(pipelines, "other"), want: false},
		{name: "same name, another resource", want: false,
			err: apierrors.NewNotFound(schema.GroupResource{Group: "kardinal.io", Resource: "bundles"}, "podinfo")},
		{name: "same name and resource, another group", want: false,
			err: apierrors.NewNotFound(schema.GroupResource{Group: "example.com", Resource: "pipelines"}, "podinfo")},
		{name: "a conflict", err: apierrors.NewConflict(pipelines, "podinfo", errors.New("changed")), want: false},
		{name: "a plain error", err: errors.New(`pipelines.kardinal.io "podinfo" not found`), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, objectgone.Is(tt.err, pipelines, "podinfo"))
		})
	}
}

func TestReconcile(t *testing.T) {
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "podinfo"}}
	other := errors.New("apiserver timeout")
	tests := []struct {
		name    string
		res     ctrl.Result
		err     error
		wantRes ctrl.Result
		wantErr error
		wantLog bool
	}{
		{name: "gone", res: ctrl.Result{RequeueAfter: time.Minute}, wantLog: true,
			err: fmt.Errorf("patch pipeline status: %w", apierrors.NewNotFound(pipelines, "podinfo"))},
		{name: "another error", res: ctrl.Result{RequeueAfter: time.Minute}, err: other,
			wantRes: ctrl.Result{RequeueAfter: time.Minute}, wantErr: other},
		{name: "another object gone", res: ctrl.Result{}, err: apierrors.NewNotFound(pipelines, "other"),
			wantErr: apierrors.NewNotFound(pipelines, "other")},
		{name: "success", res: ctrl.Result{RequeueAfter: time.Minute}, wantRes: ctrl.Result{RequeueAfter: time.Minute}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			ctx := zerolog.New(&buf).Level(zerolog.DebugLevel).WithContext(context.Background())
			res, err := objectgone.Reconcile(ctx, req, pipelines, func(context.Context, ctrl.Request) (ctrl.Result, error) {
				return tt.res, tt.err
			})
			assert.Equal(t, tt.wantRes, res)
			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				assert.Equal(t, tt.wantErr.Error(), err.Error())
			}
			if tt.wantLog {
				assert.Contains(t, buf.String(), `"level":"debug"`)
				assert.Contains(t, buf.String(), `"resource":"pipelines.kardinal.io"`)
			} else {
				assert.Empty(t, buf.String())
			}
		})
	}
}
