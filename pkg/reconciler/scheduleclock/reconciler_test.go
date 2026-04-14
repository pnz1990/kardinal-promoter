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

package scheduleclock_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/scheduleclock"
)

func TestMain(m *testing.M) { m.Run() }

// newFakeClient builds a fake client with the kardinal scheme registered.
func newFakeClient(objects ...runtime.Object) *fake.ClientBuilder {
	scheme := runtime.NewScheme()
	_ = kardinalv1alpha1.AddToScheme(scheme)
	builder := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kardinalv1alpha1.ScheduleClock{})
	for _, obj := range objects {
		builder = builder.WithRuntimeObjects(obj)
	}
	return builder
}

// makeScheduleClock creates a test ScheduleClock object.
func makeScheduleClock(interval string) *kardinalv1alpha1.ScheduleClock {
	return &kardinalv1alpha1.ScheduleClock{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kardinal-clock",
			Namespace: "kardinal-system",
		},
		Spec: kardinalv1alpha1.ScheduleClockSpec{
			Interval: interval,
		},
	}
}

// TestReconcile_WritesTick verifies the reconciler writes status.tick on each run.
func TestReconcile_WritesTick(t *testing.T) {
	fixedTime := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	clock := makeScheduleClock("1m")
	client := newFakeClient(clock).Build()

	r := &scheduleclock.Reconciler{
		Client: client,
		NowFn:  func() time.Time { return fixedTime },
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "kardinal-clock", Namespace: "kardinal-system"},
	})
	require.NoError(t, err)

	// Verify tick was written.
	var updated kardinalv1alpha1.ScheduleClock
	require.NoError(t, client.Get(context.Background(),
		types.NamespacedName{Name: "kardinal-clock", Namespace: "kardinal-system"}, &updated))
	assert.Equal(t, "2026-04-14T12:00:00Z", updated.Status.Tick)

	// Verify requeue after 1m.
	assert.Equal(t, time.Minute, result.RequeueAfter)
}

// TestReconcile_DefaultInterval verifies that missing interval defaults to 1m.
func TestReconcile_DefaultInterval(t *testing.T) {
	clock := makeScheduleClock("") // empty interval
	client := newFakeClient(clock).Build()

	r := &scheduleclock.Reconciler{
		Client: client,
		NowFn:  func() time.Time { return time.Now() },
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "kardinal-clock", Namespace: "kardinal-system"},
	})
	require.NoError(t, err)
	assert.Equal(t, time.Minute, result.RequeueAfter)
}

// TestReconcile_CustomInterval verifies custom intervals are respected.
func TestReconcile_CustomInterval(t *testing.T) {
	clock := makeScheduleClock("30s")
	client := newFakeClient(clock).Build()

	r := &scheduleclock.Reconciler{
		Client: client,
		NowFn:  func() time.Time { return time.Now() },
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "kardinal-clock", Namespace: "kardinal-system"},
	})
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, result.RequeueAfter)
}

// TestReconcile_MinimumInterval enforces the 5s minimum to prevent hot loops.
func TestReconcile_MinimumInterval(t *testing.T) {
	clock := makeScheduleClock("1s") // below minimum
	client := newFakeClient(clock).Build()

	r := &scheduleclock.Reconciler{
		Client: client,
		NowFn:  func() time.Time { return time.Now() },
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "kardinal-clock", Namespace: "kardinal-system"},
	})
	require.NoError(t, err)
	// Should be clamped to 5s minimum.
	assert.Equal(t, 5*time.Second, result.RequeueAfter)
}

// TestReconcile_NotFound verifies graceful handling of deleted ScheduleClock.
func TestReconcile_NotFound(t *testing.T) {
	client := newFakeClient().Build() // no objects

	r := &scheduleclock.Reconciler{
		Client: client,
		NowFn:  func() time.Time { return time.Now() },
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "missing", Namespace: "kardinal-system"},
	})
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

// TestReconcile_Idempotent verifies multiple reconcile calls each write a new tick.
func TestReconcile_Idempotent(t *testing.T) {
	t1 := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 14, 12, 1, 0, 0, time.UTC)
	call := 0

	clock := makeScheduleClock("1m")
	fakeClient := newFakeClient(clock).Build()

	r := &scheduleclock.Reconciler{
		Client: fakeClient,
		NowFn: func() time.Time {
			call++
			if call == 1 {
				return t1
			}
			return t2
		},
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "kardinal-clock", Namespace: "kardinal-system"},
	}

	// First reconcile.
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var first kardinalv1alpha1.ScheduleClock
	require.NoError(t, fakeClient.Get(context.Background(), req.NamespacedName, &first))
	assert.Equal(t, "2026-04-14T12:00:00Z", first.Status.Tick)

	// Second reconcile.
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var second kardinalv1alpha1.ScheduleClock
	require.NoError(t, fakeClient.Get(context.Background(), req.NamespacedName, &second))
	assert.Equal(t, "2026-04-14T12:01:00Z", second.Status.Tick)
}
