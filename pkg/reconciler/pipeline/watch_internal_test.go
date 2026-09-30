// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestPipelineForFreezeGate: a PolicyGate named freeze-<pipeline> enqueues
// that Pipeline whoever owns it, so deleting a user gate that blocks the
// freeze gate lets a paused Pipeline create it at once. Other gates enqueue
// nothing.
func TestPipelineForFreezeGate(t *testing.T) {
	tests := []struct {
		name string
		gate string
		want []ctrl.Request
	}{
		{name: "freeze gate", gate: "freeze-app",
			want: []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: "team", Name: "app"}}}},
		{name: "pipeline name with the prefix", gate: "freeze-freeze-app",
			want: []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: "team", Name: "freeze-app"}}}},
		{name: "prefix only", gate: "freeze-"},
		{name: "other gate", gate: "no-weekend-deploys"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gate := &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: tc.gate, Namespace: "team"}}
			assert.Equal(t, tc.want, pipelineForFreezeGate(context.Background(), gate))
		})
	}
}
