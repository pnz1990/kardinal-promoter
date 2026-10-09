// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// resourceHealth is a type=resource health config for the Deployment "app" in ns.
func resourceHealth(ns string) kardinalv1alpha1.HealthConfig {
	return kardinalv1alpha1.HealthConfig{Type: "resource",
		Resource: &kardinalv1alpha1.ResourceRef{Name: "app", Namespace: ns}}
}

func teamPipeline(envs ...kardinalv1alpha1.EnvironmentSpec) *kardinalv1alpha1.Pipeline {
	return &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "team-a"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
	}
}

func teamBundle(intent *kardinalv1alpha1.BundleIntent) *kardinalv1alpha1.Bundle {
	return &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-x7k2m", Namespace: "team-a", UID: "uid-x7k2m"},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "payments", Intent: intent,
			Images: []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/org/payments", Tag: "v1"}}},
	}
}

// buildWithHealth builds the Graph for p and bundle and injects health nodes
// for the environments it promotes, with mayRead as the namespace filter.
func buildWithHealth(t *testing.T, p *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle,
	mayRead func(string) bool) (*graph.Graph, map[string]string) {
	t.Helper()
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: b})
	require.NoError(t, err)
	injected := healthInjector{log: zerolog.Nop(), mayRead: mayRead}.inject(p, res.Graph, res.Environments)
	require.NoError(t, graph.ValidateNodeIDs(res.Graph.Spec.Nodes))
	return res.Graph, injected
}

// TestHealthInjector_OnlyGraphEnvironments verifies that health refs are added
// only for environments the Bundle promotes, not for those its intent filters
// out (C01-graph-17).
func TestHealthInjector_OnlyGraphEnvironments(t *testing.T) {
	tests := []struct {
		name   string
		intent *kardinalv1alpha1.BundleIntent
		want   []string
	}{
		{name: "all environments", want: []string{"healthProd", "healthStaging", "healthTest"}},
		{name: "target environment", intent: &kardinalv1alpha1.BundleIntent{TargetEnvironment: "test"},
			want: []string{"healthTest"}},
		{name: "skipped environment", intent: &kardinalv1alpha1.BundleIntent{SkipEnvironments: []string{"staging"}},
			want: []string{"healthProd", "healthTest"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := teamPipeline(
				kardinalv1alpha1.EnvironmentSpec{Name: "test", Health: resourceHealth("test")},
				kardinalv1alpha1.EnvironmentSpec{Name: "staging", Health: resourceHealth("staging")},
				kardinalv1alpha1.EnvironmentSpec{Name: "prod", Health: resourceHealth("prod")},
			)
			_, injected := buildWithHealth(t, p, teamBundle(tt.intent), nil)
			got := make([]string, 0, len(injected))
			for id := range injected {
				got = append(got, id)
			}
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

// TestHealthInjector_NodeIDCollision verifies that a health node never takes
// the node ID of an environment named health-<env> (C01-graph-18).
func TestHealthInjector_NodeIDCollision(t *testing.T) {
	p := teamPipeline(
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", Health: resourceHealth("prod")},
		kardinalv1alpha1.EnvironmentSpec{Name: "health-prod", Health: resourceHealth("health-prod")},
	)
	g, injected := buildWithHealth(t, p, teamBundle(nil), nil)
	assert.Equal(t, map[string]string{"healthProd2": "prod", "healthHealthProd": "health-prod"}, injected)
	nodes := map[string]bool{}
	for _, n := range g.Spec.Nodes {
		nodes[n.ID] = true
	}
	assert.True(t, nodes["healthProd"], "the environment health-prod keeps its step node id")
}

// TestHealthInjector_NamespaceFilter verifies that no health ref is added for
// a namespace the Graph identity may not read (C01-graph-04).
func TestHealthInjector_NamespaceFilter(t *testing.T) {
	p := teamPipeline(
		kardinalv1alpha1.EnvironmentSpec{Name: "test", Health: resourceHealth("test")},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", Health: resourceHealth("kube-system")},
	)
	prov := &graph.IdentityProvisioner{ReaderNamespaces: []string{"test"}}
	g, injected := buildWithHealth(t, p, teamBundle(nil), func(ns string) bool { return prov.MayRead("team-a", ns) })
	assert.Equal(t, map[string]string{"healthTest": "test"}, injected)
	assert.Equal(t, []string{"team-a", "test"}, graph.RefNamespaces(g), "team-a is the Bundle ref")
}

// TestDropHealthNodes verifies that only injected health nodes in the given
// namespaces are removed.
func TestDropHealthNodes(t *testing.T) {
	p := teamPipeline(
		kardinalv1alpha1.EnvironmentSpec{Name: "test", Health: resourceHealth("test")},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", Health: resourceHealth("prod")},
	)
	g, injected := buildWithHealth(t, p, teamBundle(nil), nil)
	before := len(g.Spec.Nodes)
	assert.Nil(t, dropHealthNodes(g, injected, nil))
	assert.Equal(t, []string{"healthProd"}, dropHealthNodes(g, injected, []string{"prod", "team-a"}))
	assert.Len(t, g.Spec.Nodes, before-1)
	assert.Equal(t, []string{"team-a", "test"}, graph.RefNamespaces(g), "team-a is the Bundle ref")
}

func translateScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	return s
}

// readerBindingNamespaces returns the namespaces that hold the reader
// RoleBinding of the team-a Graph identity.
func readerBindingNamespaces(t *testing.T, c client.Reader) []string {
	t.Helper()
	var list rbacv1.RoleBindingList
	require.NoError(t, c.List(context.Background(), &list))
	var out []string
	for _, rb := range list.Items {
		if rb.Name == graph.DefaultReaderClusterRole+"-team-a" {
			out = append(out, rb.Namespace)
		}
	}
	return out
}

// TestTranslate_EndToEnd runs Translate against fake clients: org gates are
// kept when the Pipeline sets spec.policyNamespaces, reader RoleBindings go
// only into allowed namespaces, refs into other namespaces are dropped, and a
// binding no Graph needs any more is deleted (C01-graph-02, C01-graph-04,
// C01-graph-19, C01-graph-31).
func TestTranslate_EndToEnd(t *testing.T) {
	ctx := zerolog.Nop().WithContext(context.Background())
	orgGate := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "no-weekend-deploys", Namespace: "platform-policies",
			Labels: map[string]string{"kardinal.io/applies-to": "prod"}},
		Spec: kardinalv1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
	}
	c := fake.NewClientBuilder().WithScheme(translateScheme(t)).WithObjects(orgGate).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, w client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if obj.GetNamespace() == "locked" {
					return apierrors.NewForbidden(schema.GroupResource{Group: "rbac.authorization.k8s.io",
						Resource: "rolebindings"}, obj.GetName(), nil)
				}
				return w.Create(ctx, obj, opts...)
			},
		}).Build()
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{graph.GraphGVR: "GraphList"})
	gc := graph.NewGraphClient(dyn, zerolog.Nop())
	tr := New(gc, graph.NewBuilder(), c, []string{"platform-policies"}, zerolog.Nop()).
		WithIdentity(&graph.IdentityProvisioner{Writer: c, Reader: c,
			ReaderNamespaces: []string{"argocd", "test", "locked"}})

	p := teamPipeline(
		kardinalv1alpha1.EnvironmentSpec{Name: "test", Health: resourceHealth("test")},
		kardinalv1alpha1.EnvironmentSpec{Name: "uat", Health: resourceHealth("kube-system")},
		kardinalv1alpha1.EnvironmentSpec{Name: "staging", Health: resourceHealth("team-b")},
		kardinalv1alpha1.EnvironmentSpec{Name: "canary", Health: resourceHealth("locked")},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "argocd"}},
	)
	p.Spec.PolicyNamespaces = []string{"team-a-extra"}
	b := teamBundle(nil)

	name, err := tr.Translate(ctx, p, b)
	require.NoError(t, err)
	g, err := gc.Get(ctx, "team-a", name)
	require.NoError(t, err)

	var gateTemplates, health []string
	for _, n := range g.Spec.Nodes {
		if n.Template != nil && n.Template["kind"] == "PolicyGate" {
			labels := n.Template["metadata"].(map[string]interface{})["labels"].(map[string]interface{})
			gateTemplates = append(gateTemplates, labels["kardinal.io/gate-template"].(string))
		}
		if n.Ref != nil && n.ID != "bundle" {
			health = append(health, n.ID)
		}
	}
	assert.Equal(t, []string{"no-weekend-deploys"}, gateTemplates,
		"spec.policyNamespaces must not drop the org gate")
	assert.ElementsMatch(t, []string{"healthTest", "healthProd"}, health,
		"refs into kube-system, team-b (not allowed) and locked (bind forbidden) must be dropped")
	// team-a is the Graph's own namespace, read through the Bundle ref.
	assert.ElementsMatch(t, []string{"argocd", "team-a", "test"}, readerBindingNamespaces(t, c))

	// The next translation no longer reads argocd: its binding is pruned.
	p.Spec.Environments[4].Health = kardinalv1alpha1.HealthConfig{}
	_, err = tr.Translate(ctx, p, b)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"team-a", "test"}, readerBindingNamespaces(t, c))

	var applier rbacv1.RoleBinding
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "team-a",
		Name: graph.DefaultApplierClusterRole}, &applier))
	assert.Equal(t, "team-a,test", applier.Annotations[graph.AnnotationReaderNamespaces])
}

// TestTranslate_PermanentErrors verifies that Translate errors caused by the
// Pipeline, Bundle or gates wrap graph.ErrInvalid, and API errors do not, so
// the Bundle reconciler can fail the one and retry the other.
func TestTranslate_PermanentErrors(t *testing.T) {
	ctx := zerolog.Nop().WithContext(context.Background())
	newTranslator := func(listErr error) *Translator {
		c := fake.NewClientBuilder().WithScheme(translateScheme(t)).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, w client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if listErr != nil {
						return listErr
					}
					return w.List(ctx, list, opts...)
				},
			}).Build()
		dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{graph.GraphGVR: "GraphList"})
		return New(graph.NewGraphClient(dyn, zerolog.Nop()), graph.NewBuilder(), c,
			[]string{"platform-policies"}, zerolog.Nop())
	}

	t.Run("invalid pipeline", func(t *testing.T) {
		p := teamPipeline(kardinalv1alpha1.EnvironmentSpec{Name: "prod",
			Steps: []kardinalv1alpha1.StepSpec{{Uses: "git-clone"}}})
		_, err := newTranslator(nil).Translate(ctx, p, teamBundle(nil))
		require.Error(t, err)
		assert.ErrorIs(t, err, graph.ErrInvalid)
	})
	t.Run("bundle with nothing to promote (#1285)", func(t *testing.T) {
		p := teamPipeline(kardinalv1alpha1.EnvironmentSpec{Name: "prod"})
		b := teamBundle(nil)
		b.Spec.Images = nil
		_, err := newTranslator(nil).Translate(ctx, p, b)
		require.Error(t, err)
		assert.ErrorIs(t, err, graph.ErrInvalid)
		assert.Contains(t, err.Error(), `type "image" requires at least one entry in images`)
	})
	t.Run("api error", func(t *testing.T) {
		p := teamPipeline(kardinalv1alpha1.EnvironmentSpec{Name: "prod"})
		_, err := newTranslator(apierrors.NewServiceUnavailable("etcd")).Translate(ctx, p, teamBundle(nil))
		require.Error(t, err)
		assert.NotErrorIs(t, err, graph.ErrInvalid)
	})
	t.Run("graph over the size limit (G10)", func(t *testing.T) {
		var envs []kardinalv1alpha1.EnvironmentSpec
		var gates []client.Object
		for i := 0; i < 300; i++ {
			name := fmt.Sprintf("region%03d", i)
			envs = append(envs, kardinalv1alpha1.EnvironmentSpec{Name: name})
			for g := 0; g < 3; g++ {
				gates = append(gates, &kardinalv1alpha1.PolicyGate{
					ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-gate%d", name, g), Namespace: "team-a",
						Labels: map[string]string{"kardinal.io/applies-to": name}},
					Spec: kardinalv1alpha1.PolicyGateSpec{Expression: `!schedule.isWeekend && upstream.uat.soakMinutes >= 30`},
				})
			}
		}
		c := fake.NewClientBuilder().WithScheme(translateScheme(t)).WithObjects(gates...).Build()
		dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{graph.GraphGVR: "GraphList"})
		tr := New(graph.NewGraphClient(dyn, zerolog.Nop()), graph.NewBuilder(), c,
			[]string{"platform-policies"}, zerolog.Nop())
		_, err := tr.Translate(ctx, teamPipeline(envs...), teamBundle(nil))
		require.Error(t, err)
		assert.ErrorIs(t, err, graph.ErrInvalid)
		assert.True(t, strings.HasPrefix(err.Error(), "translator.Translate: graph size: the Graph for this Bundle would be about "), err.Error())
		list, lerr := dyn.Resource(graph.GraphGVR).Namespace("team-a").List(ctx, metav1.ListOptions{})
		require.NoError(t, lerr)
		assert.Empty(t, list.Items, "no Graph is created")
	})
}

// TestTranslate_ErrorPrefixes checks the exact Translate error for a failure
// in each layer that names itself: the Graph builder ("build: "), the Graph
// identity ("graph identity: ") and the Graph client ("graph.Create "). Each
// context appears once (B47: "translator.Translate: build: build: ...").
func TestTranslate_ErrorPrefixes(t *testing.T) {
	ctx := zerolog.Nop().WithContext(context.Background())
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts"}, "kardinal-graph", nil)
	newTranslator := func(t *testing.T, saErr, graphErr error) *Translator {
		c := fake.NewClientBuilder().WithScheme(translateScheme(t)).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, w client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if saErr != nil {
						return saErr
					}
					return w.Create(ctx, obj, opts...)
				},
			}).Build()
		dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{graph.GraphGVR: "GraphList"})
		if graphErr != nil {
			dyn.PrependReactor("create", "graphs", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, graphErr
			})
		}
		return New(graph.NewGraphClient(dyn, zerolog.Nop()), graph.NewBuilder(), c,
			[]string{"platform-policies"}, zerolog.Nop()).
			WithIdentity(&graph.IdentityProvisioner{Writer: c, Reader: c})
	}
	prod := kardinalv1alpha1.EnvironmentSpec{Name: "prod"}

	tests := []struct {
		name            string
		pipeline        *kardinalv1alpha1.Pipeline
		saErr, graphErr error
		want            string
		once            string
	}{
		{name: "builder", pipeline: teamPipeline(),
			want: "translator.Translate: build: pipeline has no environments", once: "build:"},
		{name: "graph identity", pipeline: teamPipeline(prod), saErr: forbidden,
			want: "translator.Translate: graph identity: create serviceaccount team-a/" +
				graph.DefaultGraphServiceAccount + ": " + forbidden.Error(), once: "graph identity:"},
		{name: "graph client", pipeline: teamPipeline(prod), graphErr: errors.New("etcd unavailable"),
			want: "translator.Translate: graph.Create team-a/payments-payments-x7k2m: etcd unavailable", once: "graph.Create"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newTranslator(t, tc.saErr, tc.graphErr).Translate(ctx, tc.pipeline, teamBundle(nil))
			require.Error(t, err)
			assert.Equal(t, tc.want, err.Error())
			assert.Equal(t, 1, strings.Count(err.Error(), tc.once), "%q appears once: %v", tc.once, err)
		})
	}
}
