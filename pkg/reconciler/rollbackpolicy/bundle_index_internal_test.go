// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package rollbackpolicy

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

type bundleCounter struct {
	client.Client
	listed []int
}

func (c *bundleCounter) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	err := c.Client.List(ctx, list, opts...)
	if bl, ok := list.(*v1alpha1.BundleList); ok && err == nil {
		c.listed = append(c.listed, len(bl.Items))
	}
	return err
}

// TestExistingRollbackReadsByName (#1654, #2187 QA): the rollback lookup
// lists no Bundles: it reads the names the controller creates the automatic
// rollback at, so another pipeline's many Bundles cost nothing. A stamped
// rollback without kardinal.io/rollback-from at another name (the old
// pre-check's shape, which only a patch can now produce) is not taken.
func TestExistingRollbackReadsByName(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	stamped := func(name string) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: map[string]string{lifecycle.LabelRollback: "true"},
			Annotations: map[string]string{lifecycle.AnnotationCreatedBy: lifecycle.ControllerCreator}},
			Spec: v1alpha1.BundleSpec{Pipeline: "app", Provenance: &v1alpha1.BundleProvenance{RollbackOf: "app-bad"},
				Intent: &v1alpha1.BundleIntent{TargetEnvironment: "prod"},
				Actor:  &v1alpha1.Actor{Kind: v1alpha1.ActorController, Principal: lifecycle.ControllerCreator, Via: v1alpha1.ViaAutoRollback}}}
	}
	rp := &v1alpha1.RollbackPolicy{ObjectMeta: metav1.ObjectMeta{Name: "rp", Namespace: "ns"},
		Spec: v1alpha1.RollbackPolicySpec{PipelineName: "app", Environment: "prod", BundleRef: "app-bad"}}
	build := func(objs ...client.Object) *bundleCounter {
		objs = append(objs, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-bad", Namespace: "ns"}, Spec: v1alpha1.BundleSpec{Pipeline: "app"}})
		for i := 0; i < 40; i++ {
			objs = append(objs, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("other-%d", i), Namespace: "ns"}, Spec: v1alpha1.BundleSpec{Pipeline: "other"}})
		}
		return &bundleCounter{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
			WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()}
	}

	c := build(stamped("app-rb"))
	got, err := (&Reconciler{Client: c}).existingRollback(context.Background(), rp)
	require.NoError(t, err)
	assert.Empty(t, got, "not at a name the controller creates")
	assert.Empty(t, c.listed, "no Bundle list")

	relinked := stamped(lifecycle.AutoRollbackName("app-old", lifecycle.RollbackSourcePolicy))
	relinked.Annotations[lifecycle.AnnotationRollbackFrom] = "app-bad" // an earlier failure's, patched
	c = build(relinked)
	got, err = (&Reconciler{Client: c}).existingRollback(context.Background(), rp)
	require.NoError(t, err)
	assert.Empty(t, got, "an earlier failure's rollback relinked to app-bad is not app-bad's")

	c = build(stamped(lifecycle.AutoRollbackName("app-bad", lifecycle.RollbackSourcePolicy)))
	got, err = (&Reconciler{Client: c}).existingRollback(context.Background(), rp)
	require.NoError(t, err)
	assert.Equal(t, lifecycle.AutoRollbackName("app-bad", lifecycle.RollbackSourcePolicy), got)
	assert.Empty(t, c.listed, "no Bundle list")
}
