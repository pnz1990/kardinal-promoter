// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package auditretention_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/auditretention"
)

var now = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func event(ns, pipeline, name string, at time.Time) *v1alpha1.AuditEvent {
	ae := &v1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"kardinal.io/pipeline": pipeline}},
		Spec:       v1alpha1.AuditEventSpec{Timestamp: metav1.NewTime(at.Truncate(time.Second)), PipelineName: pipeline, Action: "PromotionStarted"},
	}
	lifecycle.StampCreatedAt(ae, at)
	return ae
}

func names(t *testing.T, c client.Client) []string {
	t.Helper()
	var list v1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list))
	var out []string
	for _, ae := range list.Items {
		out = append(out, ae.Namespace+"/"+ae.Name)
	}
	sort.Strings(out)
	return out
}

func newClient(t *testing.T, objs ...client.Object) client.WithWatch {
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

// TestPruner_MaxPerPipeline keeps the newest records of each Pipeline,
// ordered as kardinal get auditevents orders them (within a second by
// kardinal.io/created-at), and leaves other Pipelines and namespaces alone.
// A second run deletes nothing (idempotent).
func TestPruner_MaxPerPipeline(t *testing.T) {
	var objs []client.Object
	for i := 0; i < 5; i++ {
		// Five records within one second: only created-at orders them.
		objs = append(objs, event("a", "web", fmt.Sprintf("web-%d", i), now.Add(time.Duration(i)*100*time.Millisecond)))
	}
	objs = append(objs, event("a", "api", "api-0", now), event("b", "web", "b-web-0", now), event("b", "web", "b-web-1", now.Add(time.Second)))
	c := newClient(t, objs...)
	p := &auditretention.Pruner{Client: c, APIReader: c, MaxPerPipeline: 2, Now: func() time.Time { return now }}
	n, err := p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, []string{"a/api-0", "a/web-3", "a/web-4", "b/b-web-0", "b/b-web-1"}, names(t, c))

	n, err = p.Run(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left to delete")
}

// TestPruner_MaxAge deletes records older than MaxAge whatever the count;
// the off switch (both limits 0) deletes nothing; Namespace limits the run.
func TestPruner_MaxAge(t *testing.T) {
	c := newClient(t,
		event("a", "web", "old", now.Add(-100*24*time.Hour)),
		event("a", "web", "recent", now.Add(-time.Hour)),
		event("b", "web", "old-b", now.Add(-100*24*time.Hour)))
	off := &auditretention.Pruner{Client: c, APIReader: c, Now: func() time.Time { return now }}
	n, err := off.Run(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n)

	p := &auditretention.Pruner{Client: c, APIReader: c, Namespace: "a", MaxAge: auditretention.DefaultMaxAge, Now: func() time.Time { return now }}
	n, err = p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"a/recent", "b/old-b"}, names(t, c), "namespace b is outside the run")
}

// TestPruner_Pages lists past the first page (Continue).
func TestPruner_Pages(t *testing.T) {
	var objs []client.Object
	for i := 0; i < 1200; i++ {
		objs = append(objs, event("a", "web", fmt.Sprintf("e-%04d", i), now.Add(time.Duration(i)*time.Millisecond)))
	}
	c := newClient(t, objs...)
	pages := 0
	lister := interceptor.NewClient(c, interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, l client.ObjectList, opts ...client.ListOption) error {
		pages++
		return cl.List(ctx, l, opts...)
	}})
	p := &auditretention.Pruner{Client: c, APIReader: lister, MaxPerPipeline: 1000, Now: func() time.Time { return now }}
	n, err := p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 200, n, "the 200 oldest of 1200")
	assert.Len(t, names(t, c), 1000)
	assert.NotContains(t, names(t, c), "a/e-0199")
	assert.Contains(t, names(t, c), "a/e-0200")
}

// TestPruner_DeleteErrors: a record deleted meanwhile is not an error; an
// API error stops the run with the count so far.
func TestPruner_DeleteErrors(t *testing.T) {
	c := newClient(t, event("a", "web", "x", now.Add(-200*24*time.Hour)), event("a", "web", "y", now.Add(-199*24*time.Hour)))
	calls := 0
	failing := interceptor.NewClient(c, interceptor.Funcs{Delete: func(ctx context.Context, cl client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
		calls++
		if calls == 1 {
			// Gone before the pruner got to it.
			require.NoError(t, cl.Delete(ctx, o))
			return cl.Delete(ctx, o, opts...)
		}
		return errors.New("etcd unavailable")
	}})
	p := &auditretention.Pruner{Client: failing, APIReader: c, MaxAge: time.Hour, Now: func() time.Time { return now }}
	n, err := p.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "etcd unavailable")
	assert.Zero(t, n)
}

// TestPruner_LeaderOnly: retention runs on the leader only.
func TestPruner_LeaderOnly(t *testing.T) {
	assert.True(t, (&auditretention.Pruner{}).NeedLeaderElection())
}
