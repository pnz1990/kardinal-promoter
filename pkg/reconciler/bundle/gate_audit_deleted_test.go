// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"
	ctrl "sigs.k8s.io/controller-runtime"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// vanishAfterWrite makes the Graph name of the rig read as NotFound once a
// reconcile has written it (update): the Graph deleted mid-reconcile.
func vanishAfterWrite(g *gateEditRig, name string) {
	written := false
	g.dyn.PrependReactor("update", "graphs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if u, ok := a.(k8stesting.UpdateAction); ok {
			if o, ok := u.GetObject().(interface{ GetName() string }); ok && o.GetName() == name {
				written = true
			}
		}
		return false, nil, nil
	})
	g.dyn.PrependReactor("get", "graphs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if gt, ok := a.(k8stesting.GetAction); ok && written && gt.GetName() == name {
			return true, nil, apierrors.NewNotFound(graph.GraphGVR.GroupResource(), name)
		}
		return false, nil, nil
	})
}

// reconcileLogged reconciles Bundle name with a debug logger in the context
// and returns the warnings it logged.
func reconcileLogged(t *testing.T, g *gateEditRig, name string) []string {
	t.Helper()
	var buf bytes.Buffer
	ctx := zerolog.New(&buf).Level(zerolog.DebugLevel).WithContext(context.Background())
	_, err := g.r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}})
	require.NoError(t, err)
	var warns []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, `"level":"warn"`) {
			warns = append(warns, l)
		}
	}
	return warns
}

// TestGateEdit_GraphDeletedAfterWriteWarns (#2461, ENGINEER-1 on #2464): the
// gate change of a Promoting version is compared with the Graph translate has
// just written. If that Graph is gone by the re-read, it was deleted
// mid-reconcile and the change goes unrecorded: a warning, not the debug
// line of a version with no Graph to begin with.
func TestGateEdit_GraphDeletedAfterWriteWarns(t *testing.T) {
	hold := gateTemplate("hold", "prod", false)
	hold.Spec.Expression = "false"
	g := newGateEditRig(t, gateEditPipeline(), hold, gateEditBundle("app-v1", "Available", time.Now().UTC().Add(-time.Hour)))
	lcReconcile(t, g.r, "app-v1")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase)

	g.editGate(t, "hold", func(p *kardinalv1alpha1.PolicyGate) { p.Spec.Expression = "true" })
	vanishAfterWrite(g, graph.GraphNameFrom("app", "app-v1"))
	warns := reconcileLogged(t, g, "app-v1")
	require.Len(t, warns, 1, "%v", warns)
	assert.Contains(t, warns[0], "cannot read the Graph's gate copies")
	assert.Equal(t, 1, g.writes("app-v1", "update"), "translate wrote the Graph")
}

// TestRenderVersion_GraphDeletedAfterWriteWarns: the same for a Graph an
// earlier release rendered, re-rendered on upgrade (syncGraph): for it the
// record would be lost for good.
func TestRenderVersion_GraphDeletedAfterWriteWarns(t *testing.T) {
	ctx := context.Background()
	g := newGateEditRig(t, gateEditPipeline(), gateTemplate("cab", "prod", false),
		gateEditBundle("app-v1", "Available", time.Now().UTC().Add(-time.Hour)))
	lcReconcile(t, g.r, "app-v1")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase)
	name := graph.GraphNameFrom("app", "app-v1")
	gvr := g.dyn.Resource(graph.GraphGVR).Namespace("default")
	u, err := gvr.Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	labels := u.GetLabels()
	delete(labels, graph.LabelRenderVersion) // what v0.12 left
	u.SetLabels(labels)
	_, err = gvr.Update(ctx, u, metav1.UpdateOptions{})
	require.NoError(t, err)
	updates := g.writes("app-v1", "update")

	vanishAfterWrite(g, name)
	warns := reconcileLogged(t, g, "app-v1")
	require.Len(t, warns, 1, "%v", warns)
	assert.Contains(t, warns[0], "cannot read the Graph's gate copies")
	assert.Equal(t, updates+1, g.writes("app-v1", "update"), "syncGraph re-rendered the Graph")
}

// TestPipelineEdit_NoGraphBeforeIsNotAWarning (#2461): a Promoting version
// whose Graph is not there before the reconcile (a v0.8.1 version's Graph
// lives in experimental.kro.run) has no gate copies to compare when a
// Pipeline edit re-translates it: no warning (TestUpgrade_FromV081 counts
// the controller's warnings), and the Graph is written.
func TestPipelineEdit_NoGraphBeforeIsNotAWarning(t *testing.T) {
	ctx := context.Background()
	g := newGateEditRig(t, gateEditPipeline(), gateTemplate("window", "prod", false),
		gateEditBundle("app-v1", "Available", time.Now().UTC().Add(-time.Hour)))
	lcReconcile(t, g.r, "app-v1")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase)
	name := graph.GraphNameFrom("app", "app-v1")
	require.NoError(t, g.dyn.Resource(graph.GraphGVR).Namespace("default").Delete(ctx, name, metav1.DeleteOptions{}))
	var p kardinalv1alpha1.Pipeline
	require.NoError(t, g.c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "app"}, &p))
	p.Spec.Environments[1].Bake = &kardinalv1alpha1.BakeConfig{Minutes: 1}
	require.NoError(t, g.c.Update(ctx, &p))

	assert.Empty(t, reconcileLogged(t, g, "app-v1"), "no Graph before the write is not a warning")
	_, err := g.gc.Get(ctx, "default", name)
	assert.NoError(t, err, "the Graph is written again")
}
