// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package audit_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func entry(name string, at time.Time) v1alpha1.PendingAuditEvent {
	return audit.Entry(name, map[string]string{"kardinal.io/pipeline": "p"}, v1alpha1.AuditEventSpec{
		BundleName: "b", PipelineName: "p", Environment: "prod", Action: "PromotionSucceeded", Outcome: "Success",
	}, metav1.NewTime(at))
}

// TestEnqueue: an entry is stored once by name, and a full outbox drops its
// oldest entry and counts it.
//
// Covers AUDIT-OUTBOX-01.
func TestEnqueue(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 9, 12, 0, 0, 123456789, time.UTC)
	var pending []v1alpha1.PendingAuditEvent
	pending = audit.Enqueue(ctx, "Test", pending, entry("a", at))
	pending = audit.Enqueue(ctx, "Test", pending, entry("a", at))
	require.Len(t, pending, 1, "the same record is stored once")
	assert.Equal(t, "2026-10-09T12:00:00.123456789Z", pending[0].CreatedAt, "nanoseconds kept")

	before := testutil.ToFloat64(audit.Dropped.WithLabelValues("Test", "overflow"))
	for i := range v1alpha1.MaxPendingAuditEvents {
		pending = audit.Enqueue(ctx, "Test", pending, entry(fmt.Sprintf("e%d", i), at))
	}
	require.Len(t, pending, v1alpha1.MaxPendingAuditEvents)
	assert.Equal(t, "e0", pending[0].Name, "the oldest entry was dropped")
	assert.InDelta(t, before+1, testutil.ToFloat64(audit.Dropped.WithLabelValues("Test", "overflow")), 0)
}

// TestFlush: written and AlreadyExists entries leave the outbox, an invalid
// one is dropped, and one that hit an etcd timeout stays, in order, with the
// error returned.
//
// Covers AUDIT-OUTBOX-01.
func TestFlush(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	gr := schema.GroupResource{Group: "kardinal.io", Resource: "auditevents"}
	errsByName := map[string]error{
		"timeout": apierrors.NewInternalError(errors.New("etcdserver: request timed out")),
		"invalid": apierrors.NewInvalid(schema.GroupKind{Group: "kardinal.io", Kind: "AuditEvent"}, "invalid",
			field.ErrorList{field.Required(field.NewPath("spec", "action"), "")}),
		"denied": apierrors.NewForbidden(gr, "denied", nil),
	}
	c := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if err := errsByName[obj.GetName()]; err != nil {
				return err
			}
			return c.Create(ctx, obj, opts...)
		},
	}).Build()
	ctx := context.Background()
	at := time.Date(2026, 10, 9, 12, 0, 0, 5, time.UTC)
	existing := &v1alpha1.AuditEvent{ObjectMeta: metav1.ObjectMeta{Name: "exists", Namespace: "ns"}}
	require.NoError(t, c.Create(ctx, existing))

	pending := []v1alpha1.PendingAuditEvent{entry("ok", at), entry("timeout", at), entry("exists", at),
		entry("invalid", at), entry("denied", at)}
	remaining, err := audit.Flush(ctx, c, "Test", "ns", pending)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "etcdserver: request timed out")
	var names []string
	for _, p := range remaining {
		names = append(names, p.Name)
	}
	assert.Equal(t, []string{"timeout", "denied"}, names, "only retryable failures stay, in order")

	var ae v1alpha1.AuditEvent
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "ok"}, &ae))
	assert.Equal(t, "p", ae.Labels["kardinal.io/pipeline"])
	assert.True(t, at.Truncate(time.Second).Equal(ae.Spec.Timestamp.Time), "the transition time, not the write time")
	assert.Equal(t, "2026-10-09T12:00:00.000000005Z", ae.Annotations[lifecycle.AnnotationCreatedAt])
}

// TestPending is the writers' watch predicate: it fires when an update adds
// outbox entries, not when it removes them or leaves them unchanged.
func TestPending(t *testing.T) {
	at := time.Now()
	a, b := entry("a", at), entry("b", at)
	assert.True(t, audit.Pending(nil, []v1alpha1.PendingAuditEvent{a}))
	assert.True(t, audit.Pending([]v1alpha1.PendingAuditEvent{a}, []v1alpha1.PendingAuditEvent{b}))
	assert.False(t, audit.Pending([]v1alpha1.PendingAuditEvent{a}, nil))
	assert.False(t, audit.Pending([]v1alpha1.PendingAuditEvent{a}, []v1alpha1.PendingAuditEvent{a}))
}
