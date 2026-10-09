// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package auditretention_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// event is an AuditEvent created at `created` (one-second resolution, as the
// API server stores it) with kardinal.io/created-at at createdAt.
func event(ns, pipeline, name string, createdAt time.Time) *v1alpha1.AuditEvent {
	ae := &v1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"kardinal.io/pipeline": pipeline},
			CreationTimestamp: metav1.NewTime(createdAt.Truncate(time.Second))},
		Spec: v1alpha1.AuditEventSpec{Timestamp: metav1.NewTime(createdAt.Truncate(time.Second)), PipelineName: pipeline, Action: "PromotionStarted"},
	}
	lifecycle.StampCreatedAt(ae, createdAt)
	return ae
}

// pagedClient serves AuditEvent metadata lists page by page, honouring
// Limit and Continue (the fake client ignores them), counts the list calls
// and records the largest page asked for. expireAfter > 0 answers 410 Gone
// to the continue token of that page.
type pagedClient struct {
	client.WithWatch
	calls, maxLimit, expireAfter int
	deletes                      []string
	// selectors are the namespace and label selector of each list call.
	selectors []string
}

func newPaged(t *testing.T, objs ...client.Object) *pagedClient {
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return &pagedClient{WithWatch: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()}
}

func (c *pagedClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	lo := &client.ListOptions{}
	lo.ApplyOptions(opts)
	pl, ok := list.(*metav1.PartialObjectMetadataList)
	if !ok {
		return c.WithWatch.List(ctx, list, opts...)
	}
	c.calls++
	c.maxLimit = max(c.maxLimit, int(lo.Limit))
	sel := ""
	if lo.LabelSelector != nil {
		sel = lo.LabelSelector.String()
	}
	c.selectors = append(c.selectors, lo.Namespace+"|"+sel)
	var all v1alpha1.AuditEventList
	inner := []client.ListOption{}
	if lo.Namespace != "" {
		inner = append(inner, client.InNamespace(lo.Namespace))
	}
	if lo.LabelSelector != nil {
		inner = append(inner, client.MatchingLabelsSelector{Selector: lo.LabelSelector})
	}
	if err := c.WithWatch.List(ctx, &all, inner...); err != nil {
		return err
	}
	sort.Slice(all.Items, func(i, j int) bool {
		return all.Items[i].Namespace+"/"+all.Items[i].Name < all.Items[j].Namespace+"/"+all.Items[j].Name
	})
	start := 0
	if lo.Continue != "" {
		page, _ := strconv.Atoi(lo.Continue)
		if c.expireAfter > 0 && page >= c.expireAfter {
			return apierrors.NewResourceExpired("continue token expired")
		}
		start = page * int(lo.Limit)
	}
	end := min(len(all.Items), start+int(lo.Limit))
	if lo.Limit == 0 {
		end = len(all.Items)
	}
	pl.Items = nil
	for _, ae := range all.Items[start:end] {
		pl.Items = append(pl.Items, metav1.PartialObjectMetadata{ObjectMeta: ae.ObjectMeta})
	}
	pl.Continue = ""
	if end < len(all.Items) {
		pl.Continue = strconv.Itoa(end / int(lo.Limit))
	}
	return nil
}

func (c *pagedClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.deletes = append(c.deletes, obj.GetName())
	return c.WithWatch.Delete(ctx, obj, opts...)
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

// TestPruner_MaxPerPipeline keeps each Pipeline's newest records: by
// creationTimestamp, then within its second by kardinal.io/created-at
// (written in the reverse of name order here, so name order would be
// wrong); other Pipelines and namespaces are untouched; deletion runs
// oldest first; a second run deletes nothing.
//
// Covers AUDIT-RETENTION-02.
func TestPruner_MaxPerPipeline(t *testing.T) {
	var objs []client.Object
	for i := 0; i < 5; i++ {
		objs = append(objs, event("a", "web", fmt.Sprintf("web-%d", 9-i), now.Add(time.Duration(i)*100*time.Millisecond)))
	}
	objs = append(objs, event("a", "api", "api-0", now), event("b", "web", "b-web-0", now), event("b", "web", "b-web-1", now.Add(time.Second)))
	c := newPaged(t, objs...)
	p := &auditretention.Pruner{Client: c, MaxPerPipeline: 2, Now: func() time.Time { return now.Add(time.Hour) }}
	n, err := p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, []string{"a/api-0", "a/web-5", "a/web-6", "b/b-web-0", "b/b-web-1"}, names(t, c), "created-at decides within the second")
	assert.Equal(t, []string{"web-9", "web-8", "web-7"}, c.deletes, "oldest first")

	c.deletes = nil
	n, err = p.Run(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left to delete")
}

// TestPruner_MaxAge deletes records created longer ago than MaxAge while
// streaming, never one that claims a future time; the off switch (both
// limits 0) lists nothing; Namespace limits the run.
//
// Covers AUDIT-RETENTION-02.
func TestPruner_MaxAge(t *testing.T) {
	c := newPaged(t,
		event("a", "web", "old", now.Add(-100*24*time.Hour)),
		event("a", "web", "recent", now.Add(-time.Hour)),
		event("a", "web", "future", now.Add(24*time.Hour)),
		event("b", "web", "old-b", now.Add(-100*24*time.Hour)))
	off := &auditretention.Pruner{Client: c, Now: func() time.Time { return now }}
	n, err := off.Run(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Zero(t, c.calls, "off: no list at all")

	p := &auditretention.Pruner{Client: c, Namespace: "a", MaxAge: auditretention.DefaultMaxAge, Now: func() time.Time { return now }}
	n, err = p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"a/future", "a/recent", "b/old-b"}, names(t, c), "namespace b is outside the run")
}

// TestPruner_PagesAndCap: lists are paged (Limit 500, never all at once),
// the count cap lists only the Pipeline past it, and one run deletes at
// most 2000 records, the oldest first; the next run deletes the rest.
//
// Covers AUDIT-RETENTION-02.
func TestPruner_PagesAndCap(t *testing.T) {
	var objs []client.Object
	for i := 0; i < 3100; i++ {
		objs = append(objs, event("a", "web", fmt.Sprintf("e-%04d", i), now.Add(time.Duration(i)*time.Second)))
	}
	objs = append(objs, event("a", "api", "api-0", now))
	c := newPaged(t, objs...)
	p := &auditretention.Pruner{Client: c, MaxPerPipeline: 100, Now: func() time.Time { return now.Add(24 * time.Hour) }}
	n, err := p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2000, n, "at most 2000 a run")
	assert.Equal(t, 500, c.maxLimit, "pages of 500")
	assert.GreaterOrEqual(t, c.calls, 7+7, "pass 1 and the web Pipeline's pass 2, page by page")
	assert.Equal(t, "e-0000", c.deletes[0], "oldest first")
	assert.Equal(t, "e-1999", c.deletes[1999])

	n, err = p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1000, n, "the rest at the next run")
	got := names(t, c)
	assert.Len(t, got, 101)
	assert.Contains(t, got, "a/api-0")
	assert.Contains(t, got, "a/e-3000")
	assert.NotContains(t, got, "a/e-2999")
}

// TestPruner_ExpiredContinue: a 410 on an expired continue token ends the
// run without an error; the next run starts over.
//
// Covers AUDIT-RETENTION-02.
func TestPruner_ExpiredContinue(t *testing.T) {
	var objs []client.Object
	for i := 0; i < 1200; i++ {
		objs = append(objs, event("a", "web", fmt.Sprintf("e-%04d", i), now.Add(-200*24*time.Hour)))
	}
	c := newPaged(t, objs...)
	c.expireAfter = 1
	p := &auditretention.Pruner{Client: c, MaxAge: time.Hour, Now: func() time.Time { return now }}
	n, err := p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 500, n, "the first page, then the stream ends")
}

// TestPruner_DeleteErrors: a record deleted meanwhile is not an error; an
// API error stops the run with the count so far.
func TestPruner_DeleteErrors(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(event("a", "web", "x", now.Add(-200*24*time.Hour)),
		event("a", "web", "y", now.Add(-199*24*time.Hour))).Build()
	calls := 0
	c := interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, cl client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
		calls++
		if calls == 1 {
			require.NoError(t, cl.Delete(ctx, o)) // gone before the pruner got to it
			return cl.Delete(ctx, o, opts...)
		}
		return errors.New("etcd unavailable")
	}})
	p := &auditretention.Pruner{Client: c, MaxAge: time.Hour, Now: func() time.Time { return now }}
	n, err := p.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "etcd unavailable")
	assert.Zero(t, n)
}

// TestPruner_LeaderOnly: retention runs on the leader only.
func TestPruner_LeaderOnly(t *testing.T) {
	assert.True(t, (&auditretention.Pruner{}).NeedLeaderElection())
}

// TestPruner_CreationTimestampBeforeAnnotation: the count cap orders by
// metadata.creationTimestamp first; kardinal.io/created-at only orders records
// of the same second. A record whose annotation claims a later time than a
// record created a second after it is still the older one (QA #1523).
//
// Covers AUDIT-RETENTION-02.
func TestPruner_CreationTimestampBeforeAnnotation(t *testing.T) {
	older := event("a", "web", "older", now)
	lifecycle.StampCreatedAt(older, now.Add(5*time.Second)) // claims later than newer
	newer := event("a", "web", "newer", now.Add(time.Second))
	lifecycle.StampCreatedAt(newer, now)
	c := newPaged(t, older, newer)
	p := &auditretention.Pruner{Client: c, MaxPerPipeline: 1, Now: func() time.Time { return now.Add(time.Hour) }}
	n, err := p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"a/newer"}, names(t, c), "creationTimestamp wins over the annotation")
}

// TestPruner_PerPipelineSelector: the count cap lists one Pipeline's records
// with its namespace and its kardinal.io/pipeline label, not every record
// again (QA #1523).
//
// Covers AUDIT-RETENTION-02.
func TestPruner_PerPipelineSelector(t *testing.T) {
	var objs []client.Object
	for i := 0; i < 3; i++ {
		objs = append(objs, event("a", "web", fmt.Sprintf("web-%d", i), now.Add(time.Duration(i)*time.Second)))
	}
	objs = append(objs, event("a", "api", "api-0", now), event("b", "db", "db-0", now))
	c := newPaged(t, objs...)
	p := &auditretention.Pruner{Client: c, MaxPerPipeline: 1, Now: func() time.Time { return now.Add(time.Hour) }}
	_, err := p.Run(context.Background())
	require.NoError(t, err)
	require.Len(t, c.selectors, 2, "one stream of every record, then one list for the Pipeline past the cap")
	assert.Equal(t, "|", c.selectors[0], "the first pass lists every namespace without a selector")
	assert.Equal(t, "a|kardinal.io/pipeline=web", c.selectors[1])
	assert.Equal(t, []string{"a/api-0", "a/web-2", "b/db-0"}, names(t, c))
}

// TestPruner_CountCapGrace (#1552): the count cap never deletes a record
// created within the last Interval, so a burst, such as an audit outbox
// flushed after an outage, stays at least one Interval for an exporter to
// read. The records past the cap that are older go, oldest first; once the
// burst is an Interval old, the next run trims it to the cap.
//
// Covers AUDIT-RETENTION-02.
func TestPruner_CountCapGrace(t *testing.T) {
	var objs []client.Object
	for i := 0; i < 3; i++ { // old records
		objs = append(objs, event("a", "web", fmt.Sprintf("old-%d", i), now.Add(-time.Hour+time.Duration(i)*time.Second)))
	}
	for i := 0; i < 4; i++ { // a burst a minute ago
		objs = append(objs, event("a", "web", fmt.Sprintf("burst-%d", i), now.Add(-time.Minute+time.Duration(i)*time.Second)))
	}
	c := newPaged(t, objs...)
	at := now
	p := &auditretention.Pruner{Client: c, MaxPerPipeline: 2, Interval: 10 * time.Minute, Now: func() time.Time { return at }}
	n, err := p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, []string{"old-0", "old-1", "old-2"}, c.deletes, "only records older than an Interval, oldest first")
	assert.Equal(t, []string{"a/burst-0", "a/burst-1", "a/burst-2", "a/burst-3"}, names(t, c), "the burst stays past the cap")

	c.deletes = nil
	at = now.Add(10 * time.Minute)
	n, err = p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []string{"a/burst-2", "a/burst-3"}, names(t, c), "an Interval later, trimmed to the cap")
}
