// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package objectgonetest checks that a reconciler ends quietly when the object
// it reconciles is deleted while it runs (package objectgone). Tests use it
// with the controller-runtime fake client.
package objectgonetest

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// DeleteOnWrite returns interceptor funcs that delete the object of each
// status write, and of each patch or update of an object for which also
// returns true (nil: none), just before the write. The write then fails with
// the NotFound the API server returns for a deleted object. Finalizers are
// removed first, so the object is gone, not only being deleted.
func DeleteOnWrite(t testing.TB, also func(client.Object) bool) interceptor.Funcs {
	t.Helper()
	gone := func(ctx context.Context, c client.Client, obj client.Object) {
		t.Helper()
		fresh, ok := obj.DeepCopyObject().(client.Object)
		if !ok {
			t.Fatalf("%T is not a client.Object", obj)
		}
		if err := c.Get(ctx, client.ObjectKeyFromObject(obj), fresh); err != nil {
			if apierrors.IsNotFound(err) {
				return
			}
			t.Fatalf("get %s before deleting it: %v", obj.GetName(), err)
		}
		if len(fresh.GetFinalizers()) > 0 {
			fresh.SetFinalizers(nil)
			if err := c.Update(ctx, fresh); err != nil {
				t.Fatalf("remove the finalizers of %s: %v", obj.GetName(), err)
			}
		}
		if err := c.Delete(ctx, fresh); client.IgnoreNotFound(err) != nil {
			t.Fatalf("delete %s: %v", obj.GetName(), err)
		}
	}
	matches := func(obj client.Object) bool { return also != nil && also(obj) }
	return interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
			patch client.Patch, opts ...client.SubResourcePatchOption) error {
			gone(ctx, c, obj)
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object,
			opts ...client.SubResourceUpdateOption) error {
			gone(ctx, c, obj)
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch,
			opts ...client.PatchOption) error {
			if matches(obj) {
				gone(ctx, c, obj)
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if matches(obj) {
				gone(ctx, c, obj)
			}
			return c.Update(ctx, obj, opts...)
		},
	}
}

// Context returns a context whose logger writes every level, as JSON lines,
// to logs.
func Context(logs *bytes.Buffer) context.Context {
	return zerolog.New(logs).Level(zerolog.DebugLevel).WithContext(context.Background())
}

// AssertQuiet checks that a reconcile ended as for an object that is gone: no
// error, an empty result, and no warn or error line in logs about a NotFound.
// Warnings about something else, such as the failed poll a test sets up, are
// allowed.
func AssertQuiet(t testing.TB, res ctrl.Result, err error, logs *bytes.Buffer) {
	t.Helper()
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, res)
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry struct {
			Level string `json:"level"`
		}
		if line == "" || json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if (entry.Level == "warn" || entry.Level == "error") && strings.Contains(line, "not found") {
			assert.Failf(t, "a NotFound logged at "+entry.Level, "%s", line)
		}
	}
}
