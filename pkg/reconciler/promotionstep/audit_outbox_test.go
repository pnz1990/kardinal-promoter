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

package promotionstep_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// etcdTimeout is the error the API server returns when etcd does not answer
// in time, as the scale suite saw it ("etcdserver: request timed out").
var etcdTimeout = apierrors.NewInternalError(errors.New("etcdserver: request timed out"))

// faultyAuditClient is a fake client whose AuditEvent creates fail while
// failCreates is set, and whose next status patch that removes outbox
// entries of a Verified step fails once when failClear is set.
type faultyAuditClient struct {
	client.Client
	failCreates atomic.Bool
	failClear   atomic.Bool
}

func newFaultyAuditClient(t *testing.T, objs ...client.Object) *faultyAuditClient {
	t.Helper()
	f := &faultyAuditClient{}
	f.Client = fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}, &v1alpha1.Bundle{}).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*v1alpha1.AuditEvent); ok && f.failCreates.Load() {
					return etcdTimeout
				}
				return c.Create(ctx, obj, opts...)
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
				patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if ps, ok := obj.(*v1alpha1.PromotionStep); ok && ps.Status.State == "Verified" &&
					len(ps.Status.PendingAuditEvents) == 0 &&
					f.failClear.CompareAndSwap(true, false) {
					return etcdTimeout
				}
				return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	return f
}

// TestAuditOutbox_EtcdTimeout proves #1552: an AuditEvent create that fails
// with an etcd timeout no longer loses the record. The transition to
// Verified stores the record in status.pendingAuditEvents in the same patch
// as the state; the step is requeued, and once creates succeed the next
// reconcile writes the AuditEvent and empties the outbox.
//
// Covers AUDIT-OUTBOX-01.
func TestAuditOutbox_EtcdTimeout(t *testing.T) {
	c, reconcile := outboxCase(t)

	c.failCreates.Store(true)
	res := reconcile()
	st := getStep(t, c, "step")
	require.Equal(t, "Verified", st.Status.State, "the audit write never blocks the promotion")
	require.Len(t, st.Status.PendingAuditEvents, 1, "the record is stored with the transition")
	assert.Equal(t, "step-succeeded", st.Status.PendingAuditEvents[0].Name)
	assert.Equal(t, "PromotionSucceeded", st.Status.PendingAuditEvents[0].Spec.Action)
	assert.Empty(t, auditActions(t, c), "etcd timed out: nothing written yet")
	assert.Positive(t, res.RequeueAfter, "a Verified step is requeued while its outbox holds records")

	// Still failing: the record stays, the step stays requeued.
	res = reconcile()
	assert.Len(t, getStep(t, c, "step").Status.PendingAuditEvents, 1)
	assert.Positive(t, res.RequeueAfter)

	c.failCreates.Store(false)
	res = reconcile()
	assert.Equal(t, []string{"PromotionSucceeded"}, auditActions(t, c))
	assert.Empty(t, getStep(t, c, "step").Status.PendingAuditEvents, "written records leave the outbox")
	assert.Zero(t, res.RequeueAfter, "nothing left to retry")
}

// TestAuditOutbox_WrittenButNotCleared: the create succeeds but the status
// patch removing the entry times out, so the next reconcile creates it
// again. AlreadyExists counts as written: one AuditEvent, an empty outbox.
//
// Covers AUDIT-OUTBOX-01.
func TestAuditOutbox_WrittenButNotCleared(t *testing.T) {
	c, reconcile := outboxCase(t)

	c.failClear.Store(true)
	res := reconcile()
	st := getStep(t, c, "step")
	require.Equal(t, "Verified", st.Status.State)
	assert.Equal(t, []string{"PromotionSucceeded"}, auditActions(t, c))
	assert.Len(t, st.Status.PendingAuditEvents, 1, "the clear patch timed out")
	assert.Positive(t, res.RequeueAfter)

	reconcile()
	assert.Equal(t, []string{"PromotionSucceeded"}, auditActions(t, c), "no duplicate")
	assert.Empty(t, getStep(t, c, "step").Status.PendingAuditEvents)
}

// TestAuditOutbox_Restart: a controller that dies right after the state
// patch never runs its flush. A later reconcile, by any replica, finds the
// record in the step's status and writes it.
//
// Covers AUDIT-OUTBOX-01.
func TestAuditOutbox_Restart(t *testing.T) {
	c, reconcile := outboxCase(t)
	st := getStep(t, c, "step")
	st.Status.State = "Verified"
	st.Status.PendingAuditEvents = []v1alpha1.PendingAuditEvent{{
		Name:   "step-succeeded",
		Labels: map[string]string{"kardinal.io/pipeline": "p", "kardinal.io/bundle": "b1", "kardinal.io/action": "PromotionSucceeded"},
		Spec: v1alpha1.AuditEventSpec{BundleName: "b1", PipelineName: "p", Environment: "test",
			Action: "PromotionSucceeded", Outcome: "Success", Timestamp: st.CreationTimestamp},
	}}
	require.NoError(t, c.Status().Update(context.Background(), &st))

	reconcile()
	assert.Equal(t, []string{"PromotionSucceeded"}, auditActions(t, c))
	assert.Empty(t, getStep(t, c, "step").Status.PendingAuditEvents)
}

// outboxCase is a HealthChecking step whose Deployment is healthy, so its
// next reconcile verifies it, and a reconcile function returning the result.
func outboxCase(t *testing.T) (*faultyAuditClient, func() ctrl.Result) {
	t.Helper()
	pipeline := makePipeline("p")
	pipeline.Spec.Environments = []v1alpha1.EnvironmentSpec{
		{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource"}}}
	ps := labelled(makeStep("step", "p", "b1", "test"))
	ps.Status.State = "HealthChecking"
	c := newFaultyAuditClient(t, pipeline, makeBundle("b1", "p"), ps, healthyDeployment("p", "test"))
	return c, func() ctrl.Result {
		t.Helper()
		r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
			HealthDetector: health.NewAutoDetector(c, dynfake.NewSimpleDynamicClient(runtime.NewScheme()))}
		res, err := r.Reconcile(context.Background(), reqFor("step"))
		require.NoError(t, err)
		return res
	}
}
