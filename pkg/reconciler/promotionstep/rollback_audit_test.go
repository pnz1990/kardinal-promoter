// Copyright 2026 The kardinal-promoter Authors.
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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// rollbackOf marks Bundle b1 as the rollback that restores the artifacts of
// b0 after b2 failed, the way lifecycle.PlanRollback does.
func rollbackOf(b *v1alpha1.Bundle) {
	b.Labels = map[string]string{lifecycle.LabelRollback: "true"}
	b.Annotations = map[string]string{lifecycle.AnnotationRollbackFrom: "b2"}
	b.Spec.Provenance = &v1alpha1.BundleProvenance{RollbackOf: "b0"}
}

// auditMessages returns the message of every AuditEvent with action.
func auditMessages(t *testing.T, c client.Client, action string) []string {
	t.Helper()
	var list v1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace("default")))
	var out []string
	for _, ae := range list.Items {
		if ae.Spec.Action == action {
			out = append(out, ae.Spec.Message)
		}
	}
	return out
}

// TestRollbackSucceededAudit covers B50: the AuditEvent action
// RollbackSucceeded was in the CRD enum but never written. A step of a
// rollback Bundle that reaches Verified now writes it, besides
// PromotionSucceeded; a step of any other Bundle writes PromotionSucceeded
// only.
func TestRollbackSucceededAudit(t *testing.T) {
	env := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource"}}
	bakeEnv := env
	bakeEnv.Bake = &v1alpha1.BakeConfig{Minutes: 5, Policy: "reset-on-alarm"}
	started := metav1.NewTime(time.Now().Add(-6 * time.Minute))
	tests := []struct {
		name      string
		env       v1alpha1.EnvironmentSpec
		status    v1alpha1.PromotionStepStatus
		bundle    func(*v1alpha1.Bundle)
		wantAudit []string
		wantMsg   string
	}{
		{name: "rollback Bundle verified by the health check", env: env, bundle: rollbackOf,
			wantAudit: []string{"PromotionSucceeded", "RollbackSucceeded"},
			wantMsg:   "rollback Bundle b1 (artifacts of b0, rolled back from b2) verified in test: "},
		{name: "rollback Bundle verified by the bake", env: bakeEnv, bundle: rollbackOf,
			status:    v1alpha1.PromotionStepStatus{BakeStartedAt: &started},
			wantAudit: []string{"PromotionSucceeded", "RollbackSucceeded"},
			wantMsg:   "rollback Bundle b1 (artifacts of b0, rolled back from b2) verified in test: bake complete"},
		{name: "rollback marked by provenance only", env: env,
			bundle: func(b *v1alpha1.Bundle) {
				b.Spec.Provenance = &v1alpha1.BundleProvenance{RollbackOf: "b0"}
			},
			wantAudit: []string{"PromotionSucceeded", "RollbackSucceeded"},
			wantMsg:   "rollback Bundle b1 (artifacts of b0) verified in test: "},
		{name: "rollback marked by the label only", env: env,
			bundle: func(b *v1alpha1.Bundle) {
				b.Labels = map[string]string{lifecycle.LabelRollback: "true"}
			},
			wantAudit: []string{"PromotionSucceeded", "RollbackSucceeded"},
			wantMsg:   "rollback Bundle b1 verified in test: "},
		{name: "not a rollback", env: env, wantAudit: []string{"PromotionSucceeded"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, got, _ := healthCase{env: tt.env, status: tt.status, bundle: tt.bundle,
				objs: []client.Object{healthyDeployment("p", "test")}}.run(t)
			require.Equal(t, "Verified", got.Status.State, got.Status.Message)
			assert.Equal(t, tt.wantAudit, auditActions(t, c))
			msgs := auditMessages(t, c, "RollbackSucceeded")
			if tt.wantMsg == "" {
				assert.Empty(t, msgs)
				return
			}
			require.Len(t, msgs, 1)
			assert.Contains(t, msgs[0], tt.wantMsg)
			var ae v1alpha1.AuditEvent
			require.NoError(t, c.Get(context.Background(),
				client.ObjectKey{Namespace: "default", Name: "step-rollback-succeeded"}, &ae))
			assert.Equal(t, "Success", ae.Spec.Outcome)
			assert.Equal(t, "b1", ae.Spec.BundleName)
			assert.Equal(t, "test", ae.Spec.Environment)
			assert.Equal(t, "RollbackSucceeded", ae.Labels["kardinal.io/action"])
		})
	}
}

// TestRollbackSucceededAudit_Once: a step writes one RollbackSucceeded
// AuditEvent. Reconciling the Verified step again writes nothing, and a
// reconcile that repeats the transition (a controller restarted on a stale
// read of the step) finds the AuditEvent by name.
func TestRollbackSucceededAudit_Once(t *testing.T) {
	pipeline := makePipeline("p")
	pipeline.Spec.Environments = []v1alpha1.EnvironmentSpec{
		{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource"}}}
	bundle := makeBundle("b1", "p")
	rollbackOf(bundle)
	ps := labelled(makeStep("step", "p", "b1", "test"))
	ps.Status.State = "HealthChecking"
	c := newClient(t, pipeline, bundle, ps, healthyDeployment("p", "test"))
	reconcile := func() {
		r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
			HealthDetector: health.NewAutoDetector(c, dynfake.NewSimpleDynamicClient(runtime.NewScheme()))}
		_, err := r.Reconcile(context.Background(), reqFor("step"))
		require.NoError(t, err)
	}

	reconcile()
	require.Equal(t, "Verified", getStep(t, c, "step").Status.State)
	reconcile()
	assert.Len(t, auditMessages(t, c, "RollbackSucceeded"), 1, "a Verified step writes no second event")

	// Repeat the transition, as after a restart that read the step before
	// it was Verified.
	st := getStep(t, c, "step")
	st.Status.State = "HealthChecking"
	st.Status.LastHealthCheckAt = nil
	require.NoError(t, c.Status().Update(context.Background(), &st))
	reconcile()
	require.Equal(t, "Verified", getStep(t, c, "step").Status.State)
	assert.Len(t, auditMessages(t, c, "RollbackSucceeded"), 1, "one RollbackSucceeded per step")
	assert.Equal(t, []string{"PromotionSucceeded", "RollbackSucceeded"}, auditActions(t, c))
}
