//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestAudit_Retention runs the controller with small retention limits
// (--audit-retention-max-per-pipeline=3, --audit-retention-max-age=24h,
// --audit-retention-interval=5s) and checks that the leader deletes a
// Pipeline's records past the 3 newest, in kardinal get auditevents order
// (within one second by kardinal.io/created-at), and a record of another
// Pipeline older than a day, and keeps the rest. It restarts the
// controller, so it is not parallel. The chart's defaults (90 days, 1000)
// and the off switch are checked in test/helm.
//
// Covers AUDIT-RETENTION-01.
func TestAudit_Retention(t *testing.T) {
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	now := time.Now().UTC()
	create := func(pipeline, name string, at time.Time) {
		ae := &v1alpha1.AuditEvent{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"kardinal.io/pipeline": pipeline}},
			Spec: v1alpha1.AuditEventSpec{Timestamp: metav1.NewTime(at), PipelineName: pipeline, BundleName: pipeline + "-v1",
				Environment: "prod", Action: "PromotionStarted", Outcome: "Pending", Message: "e2e retention"},
		}
		lifecycle.StampCreatedAt(ae, at)
		require.NoError(t, e.Client.Create(ctx, ae))
	}
	for i := 0; i < 6; i++ {
		// Six records within one second: created-at orders them.
		create("web", fmt.Sprintf("web-%d", i), now.Add(time.Duration(i)*100*time.Millisecond))
	}
	create("api", "api-old", now.Add(-48*time.Hour))
	create("api", "api-new", now)

	restore := e.PatchController(t, func(spec *corev1.PodSpec) {
		framework.SetArg(spec, "audit-retention-max-per-pipeline", "3")
		framework.SetArg(spec, "audit-retention-max-age", "24h")
		framework.SetArg(spec, "audit-retention-interval", "5s")
	})
	defer restore()

	want := []string{"api-new", "web-3", "web-4", "web-5"}
	var got []string
	framework.Eventually(t, 2*time.Minute, "retention to delete the old records", func(ctx context.Context) (bool, string) {
		var list v1alpha1.AuditEventList
		if err := e.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
			return false, err.Error()
		}
		got = got[:0]
		for _, ae := range list.Items {
			got = append(got, ae.Name)
		}
		sort.Strings(got)
		return strings.Join(got, ",") == strings.Join(want, ","), strings.Join(got, ",")
	})
	assert.Equal(t, want, got)
	e.WaitControllerLog(t, now.Add(-time.Minute), time.Minute, "the retention run logged",
		framework.LogMessage("AuditEvent retention deleted old records"))
}
