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

package notificationhook_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/notificationhook"
)

func nhScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

func reqFor(namespace, name string) ctrl.Request {
	return reconcile.Request{NamespacedName: k8stypes.NamespacedName{Namespace: namespace, Name: name}}
}

// TestReconcile_BundleVerified verifies that a Bundle.Verified event triggers delivery (O3).
func TestReconcile_BundleVerified(t *testing.T) {
	received := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hook := &v1alpha1.NotificationHook{
		ObjectMeta: metav1.ObjectMeta{Name: "test-hook", Namespace: "default"},
		Spec: v1alpha1.NotificationHookSpec{
			Webhook: v1alpha1.NotificationWebhookConfig{URL: srv.URL},
			Events:  []v1alpha1.NotificationHookEventType{v1alpha1.NotificationEventBundleVerified},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-bundle",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Now()},
		},
		Spec:   v1alpha1.BundleSpec{Pipeline: "nginx-demo"},
		Status: v1alpha1.BundleStatus{Phase: "Verified"},
	}

	c := fake.NewClientBuilder().WithScheme(nhScheme()).
		WithObjects(hook, bundle).
		WithStatusSubresource(&v1alpha1.NotificationHook{}).
		Build()

	r := &notificationhook.Reconciler{Client: c}
	result, err := r.Reconcile(context.Background(), reqFor("default", "test-hook"))
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	select {
	case body := <-received:
		assert.NotEmpty(t, body)
		var payload map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "Bundle.Verified", payload["event"])
		assert.Equal(t, "nginx-demo", payload["pipeline"])
		assert.Equal(t, "my-bundle", payload["bundle"])
	case <-time.After(2 * time.Second):
		t.Fatal("webhook not delivered")
	}
}

// TestReconcile_BundleFailed verifies that a Bundle.Failed event triggers delivery (O4).
func TestReconcile_BundleFailed(t *testing.T) {
	received := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hook := &v1alpha1.NotificationHook{
		ObjectMeta: metav1.ObjectMeta{Name: "test-hook", Namespace: "default"},
		Spec: v1alpha1.NotificationHookSpec{
			Webhook: v1alpha1.NotificationWebhookConfig{URL: srv.URL},
			Events:  []v1alpha1.NotificationHookEventType{v1alpha1.NotificationEventBundleFailed},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "failed-bundle",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Now()},
		},
		Status: v1alpha1.BundleStatus{Phase: "Failed"},
	}

	c := fake.NewClientBuilder().WithScheme(nhScheme()).
		WithObjects(hook, bundle).
		WithStatusSubresource(&v1alpha1.NotificationHook{}).
		Build()

	r := &notificationhook.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), reqFor("default", "test-hook"))
	require.NoError(t, err)

	select {
	case body := <-received:
		var payload map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "Bundle.Failed", payload["event"])
	case <-time.After(2 * time.Second):
		t.Fatal("webhook not delivered")
	}
}

// TestReconcile_Idempotency verifies that a delivered event is not re-delivered (O7).
func TestReconcile_Idempotency(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	bundleName := "my-bundle"
	eventKey := "Bundle.Verified/" + bundleName

	hook := &v1alpha1.NotificationHook{
		ObjectMeta: metav1.ObjectMeta{Name: "test-hook", Namespace: "default"},
		Spec: v1alpha1.NotificationHookSpec{
			Webhook: v1alpha1.NotificationWebhookConfig{URL: srv.URL},
			Events:  []v1alpha1.NotificationHookEventType{v1alpha1.NotificationEventBundleVerified},
		},
		Status: v1alpha1.NotificationHookStatus{
			LastEventKey: eventKey,
			LastSentAt:   time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              bundleName,
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Now().Add(-10 * time.Minute)},
		},
		Spec:   v1alpha1.BundleSpec{Pipeline: "nginx-demo"},
		Status: v1alpha1.BundleStatus{Phase: "Verified"},
	}

	c := fake.NewClientBuilder().WithScheme(nhScheme()).
		WithObjects(hook, bundle).
		WithStatusSubresource(&v1alpha1.NotificationHook{}).
		Build()

	r := &notificationhook.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), reqFor("default", "test-hook"))
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), reqFor("default", "test-hook"))
	require.NoError(t, err)

	assert.Equal(t, 0, callCount, "webhook must not fire when event was already delivered (O7)")
}

// TestReconcile_PayloadShape verifies the webhook payload has required fields (O8).
func TestReconcile_PayloadShape(t *testing.T) {
	received := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hook := &v1alpha1.NotificationHook{
		ObjectMeta: metav1.ObjectMeta{Name: "test-hook", Namespace: "default"},
		Spec: v1alpha1.NotificationHookSpec{
			Webhook: v1alpha1.NotificationWebhookConfig{URL: srv.URL},
			Events:  []v1alpha1.NotificationHookEventType{v1alpha1.NotificationEventBundleVerified},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "payload-bundle",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Now()},
		},
		Spec:   v1alpha1.BundleSpec{Pipeline: "test-pipeline"},
		Status: v1alpha1.BundleStatus{Phase: "Verified"},
	}

	c := fake.NewClientBuilder().WithScheme(nhScheme()).
		WithObjects(hook, bundle).
		WithStatusSubresource(&v1alpha1.NotificationHook{}).
		Build()

	r := &notificationhook.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), reqFor("default", "test-hook"))
	require.NoError(t, err)

	select {
	case body := <-received:
		var payload map[string]interface{}
		require.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "Bundle.Verified", payload["event"])
		assert.Equal(t, "test-pipeline", payload["pipeline"])
		assert.Equal(t, "payload-bundle", payload["bundle"])
		assert.NotEmpty(t, payload["message"])
		assert.NotEmpty(t, payload["timestamp"])
		_, err := time.Parse(time.RFC3339, payload["timestamp"].(string))
		assert.NoError(t, err, "timestamp must be RFC3339")
	case <-time.After(2 * time.Second):
		t.Fatal("webhook not delivered")
	}
}

// TestReconcile_NoMatchingEvent verifies that no webhook fires when no qualifying event exists.
func TestReconcile_NoMatchingEvent(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hook := &v1alpha1.NotificationHook{
		ObjectMeta: metav1.ObjectMeta{Name: "test-hook", Namespace: "default"},
		Spec: v1alpha1.NotificationHookSpec{
			Webhook: v1alpha1.NotificationWebhookConfig{URL: srv.URL},
			Events:  []v1alpha1.NotificationHookEventType{v1alpha1.NotificationEventBundleVerified},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "promoting-bundle", Namespace: "default"},
		Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
	}

	c := fake.NewClientBuilder().WithScheme(nhScheme()).
		WithObjects(hook, bundle).
		WithStatusSubresource(&v1alpha1.NotificationHook{}).
		Build()

	r := &notificationhook.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), reqFor("default", "test-hook"))
	require.NoError(t, err)

	assert.Equal(t, 0, callCount, "no webhook must fire when no qualifying event exists")
}
