// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
)

// recordingManager records the names of the controllers added to it.
type recordingManager struct {
	manager.Manager
	names []string
}

func (m *recordingManager) Add(r manager.Runnable) error {
	if v := reflect.ValueOf(r); v.Kind() == reflect.Pointer && v.Elem().Kind() == reflect.Struct {
		if f := v.Elem().FieldByName("Name"); f.IsValid() && f.Kind() == reflect.String {
			m.names = append(m.names, f.String())
		}
	}
	return m.Manager.Add(r)
}

// TestSetupWithManager_RetireRegisteredWithZeroPolicy (QA #1527): the
// retirement controller is registered even when every delay of the policy is
// 0 (keep every Graph), because a Pipeline's kardinal.io/graph-retire-after
// annotation can still ask for retirement.
func TestSetupWithManager_RetireRegisteredWithZeroPolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(scheme))
	informers := &informertest.FakeInformers{Scheme: scheme}
	mgr, err := ctrl.NewManager(&rest.Config{Host: "https://127.0.0.1:1"}, ctrl.Options{
		Scheme:                 scheme,
		NewCache:               func(*rest.Config, cache.Options) (cache.Cache, error) { return informers, nil },
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	rec := &recordingManager{Manager: mgr}
	r := &bundle.Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Retire: bundle.RetirePolicy{}}
	require.NoError(t, r.SetupWithManager(rec))
	assert.Contains(t, rec.names, "bundle-retire", "registered controllers: %v", rec.names)
	assert.Contains(t, rec.names, "bundle")
}
