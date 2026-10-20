// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestDoctor_ReceiptGrowth (#2136): doctor reports the receipts' count and
// size from the Pipelines' status, and warns past 1 GiB when the controller
// keeps every receipt (no --receipt-retention-max-age); with retention, or
// below the threshold, or without receipts, it passes.
//
// Covers RECEIPT-SIZE-01.
func TestDoctor_ReceiptGrowth(t *testing.T) {
	pipeline := func(bytes int64) *v1alpha1.Pipeline {
		return &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"},
			Status: v1alpha1.PipelineStatus{Environments: []v1alpha1.PipelineEnvironmentStatus{
				{Name: "test", Receipts: &v1alpha1.EnvironmentReceipts{Count: 3000, Bytes: bytes / 2}},
				{Name: "prod", Receipts: &v1alpha1.EnvironmentReceipts{Count: 1000, Bytes: bytes / 2}},
			}}}
	}
	controller := func(args ...string) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter", Namespace: "kardinal-system"},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
				{Name: "controller", Args: append([]string{"--receipts=true"}, args...)}}}}}}
	}
	for name, tc := range map[string]struct {
		objs   []sigs_client.Object
		warned bool
		detail string
	}{
		"over 1 GiB, retention off": {[]sigs_client.Object{pipeline(2 << 30), controller()}, true, "4000 receipts, 2.0 GiB (all namespaces; retention off)"},
		"over 1 GiB, retention on":  {[]sigs_client.Object{pipeline(2 << 30), controller("--receipt-retention-max-age=2160h")}, false, "retention 2160h"},
		// QA-2 #2174: the flag as the controller reads it.
		"retention on, space-separated": {[]sigs_client.Object{pipeline(2 << 30), controller("--receipt-retention-max-age", "720h")}, false, "retention 720h"},
		"retention 0h is off":           {[]sigs_client.Object{pipeline(2 << 30), controller("--receipt-retention-max-age=0h")}, true, "retention off"},
		"retention 0m is off":           {[]sigs_client.Object{pipeline(2 << 30), controller("--receipt-retention-max-age", "0m")}, true, "retention off"},
		"retention unparseable is off":  {[]sigs_client.Object{pipeline(2 << 30), controller("--receipt-retention-max-age=30d")}, true, "retention off"},
		"the last value wins":           {[]sigs_client.Object{pipeline(2 << 30), controller("--receipt-retention-max-age=720h", "--receipt-retention-max-age=0")}, true, "retention off"},
		"under 1 GiB":                   {[]sigs_client.Object{pipeline(10 << 20), controller()}, false, "4000 receipts, 10.0 MiB"},
		"no receipts":                   {[]sigs_client.Object{controller()}, false, "no promotion receipts"},
		"controller unreadable":         {[]sigs_client.Object{pipeline(2 << 30)}, false, "retention unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(tc.objs...).Build()
			r, shown := checkReceiptGrowth(context.Background(), c, "team-a", "kardinal-system")
			assert.True(t, shown, "receipts exist or the controller signs them")
			assert.Equal(t, tc.warned, r.warned, r.detail)
			assert.Contains(t, r.detail, tc.detail)
			if tc.warned {
				assert.Contains(t, r.hint, "receipts.retention.maxAge")
			}
		})
	}
}

// TestDoctor_ReceiptGrowthHiddenWithoutReceipts (#2174 live core): a
// cluster whose controller does not sign receipts and has none sees no
// "Receipt storage" line, so doctor's output is unchanged for it; one with
// receipts, or whose controller signs them, does.
func TestDoctor_ReceiptGrowthHiddenWithoutReceipts(t *testing.T) {
	controller := func(args ...string) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter", Namespace: "kardinal-system"},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
				{Name: "controller", Args: args}}}}}}
	}
	withReceipts := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"},
		Status: v1alpha1.PipelineStatus{Environments: []v1alpha1.PipelineEnvironmentStatus{
			{Name: "prod", Receipts: &v1alpha1.EnvironmentReceipts{Count: 1, Bytes: 4096}}}}}
	for name, tc := range map[string]struct {
		objs  []sigs_client.Object
		shown bool
	}{
		"receipts off, none":       {[]sigs_client.Object{controller("--receipts=false")}, false},
		"no receipts flag, none":   {[]sigs_client.Object{controller()}, false},
		"controller unreadable":    {nil, false},
		"receipts on, none yet":    {[]sigs_client.Object{controller("--receipts=true")}, true},
		"receipts off, some exist": {[]sigs_client.Object{controller("--receipts=false"), withReceipts}, true},
	} {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(tc.objs...).Build()
			_, shown := checkReceiptGrowth(context.Background(), c, "team-a", "kardinal-system")
			assert.Equal(t, tc.shown, shown)
		})
	}
}
