// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// TestPolicyNamespacesAgreed (#2286): a new version of a Pipeline whose
// spec.policyNamespaces names a namespace that must agree is translated
// only from a status.policyNamespaces for the Pipeline's generation that
// does not name it; the Pipeline's own namespace and the org policy
// namespaces need nothing. Covers GATE-POLNS-05.
func TestPolicyNamespacesAgreed(t *testing.T) {
	checked := func(gen int64, notShared ...string) *kardinalv1alpha1.PipelinePolicyNamespaces {
		return &kardinalv1alpha1.PipelinePolicyNamespaces{ObservedGeneration: gen, NotShared: notShared}
	}
	for name, tc := range map[string]struct {
		spec      []string
		status    *kardinalv1alpha1.PipelinePolicyNamespaces
		refused   []string
		unchecked bool
	}{
		"none":                                {},
		"own and org namespaces need nothing": {spec: []string{"team-a", "platform-policies"}},
		"agreed":                              {spec: []string{"shared"}, status: checked(3)},
		"not checked yet":                     {spec: []string{"shared"}, unchecked: true},
		"checked for an older generation":     {spec: []string{"shared"}, status: checked(2), unchecked: true},
		"refused":                             {spec: []string{"team-b", "shared", "team-c"}, status: checked(3, "team-b", "team-c"), refused: []string{"team-b", "team-c"}},
		"a name the status no longer lists":   {spec: []string{"shared"}, status: checked(3, "gone")},
	} {
		t.Run(name, func(t *testing.T) {
			p := &kardinalv1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a", Generation: 3},
				Spec: kardinalv1alpha1.PipelineSpec{PolicyNamespaces: tc.spec}, Status: kardinalv1alpha1.PipelineStatus{PolicyNamespaces: tc.status}}
			err := PolicyNamespacesAgreed(p, []string{"platform-policies"})
			if tc.refused == nil && !tc.unchecked {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, IsPolicyNamespacesError(err))
			var e *PolicyNamespacesError
			require.ErrorAs(t, err, &e)
			assert.Equal(t, tc.unchecked, e.Unchecked)
			assert.Equal(t, tc.refused, e.NotShared)
			for _, ns := range tc.refused {
				assert.Contains(t, err.Error(), ns)
			}
		})
	}
}

// TestTranslate_RefusesNotSharedNamespace (#2286): Translate refuses a new
// version whose Pipeline names a namespace that has not agreed, without
// listing that namespace's PolicyGates. Covers GATE-POLNS-05.
func TestTranslate_RefusesNotSharedNamespace(t *testing.T) {
	ctx := zerolog.Nop().WithContext(context.Background())
	listed := map[string]bool{}
	c := fake.NewClientBuilder().WithScheme(translateScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			o := &client.ListOptions{}
			o.ApplyOptions(opts)
			if _, ok := list.(*kardinalv1alpha1.PolicyGateList); ok {
				listed[o.Namespace] = true
			}
			return cl.List(ctx, list, opts...)
		}}).Build()
	tr := New(nil, graph.NewBuilder(), c, []string{"platform-policies"}, zerolog.Nop())
	p := teamPipeline(kardinalv1alpha1.EnvironmentSpec{Name: "test"})
	p.Generation = 4
	p.Spec.PolicyNamespaces = []string{"team-b"}
	p.Status.PolicyNamespaces = &kardinalv1alpha1.PipelinePolicyNamespaces{ObservedGeneration: 4, NotShared: []string{"team-b"}}
	_, err := tr.Translate(ctx, p, teamBundle(nil))
	require.Error(t, err)
	assert.True(t, IsPolicyNamespacesError(err))
	assert.False(t, listed["team-b"], "team-b's gates are not read")
}
