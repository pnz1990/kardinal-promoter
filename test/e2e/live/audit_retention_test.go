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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestAudit_Retention runs the controller with small retention limits
// (--audit-retention=true, --audit-retention-max-per-pipeline=3, --audit-retention-max-age=24h,
// --audit-retention-interval=5s) and checks that the leader deletes a
// Pipeline's records past the 3 newest (within one second by
// kardinal.io/created-at) and keeps the rest, the other Pipeline's records
// and a recent record whose spec.timestamp claims an old time included (age
// is metadata.creationTimestamp). It grants the controller delete on
// AuditEvents in its namespace (the chart does with
// audit.retention.enabled) and restarts the
// controller, so it is not parallel. The chart's defaults (on, 90 days,
// 1000) and the off switch are checked in test/helm.
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

	// The chart grants delete on AuditEvents with audit.retention.enabled
	// (the default); grant it in the test's namespace too, so the test does
	// not depend on the install's value.
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "audit-retention"},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{"kardinal.io"}, Resources: []string{"auditevents"}, Verbs: []string{"delete"}}}}
	_, err := e.Kube.RbacV1().Roles(ns).Create(ctx, role, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = e.Kube.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "audit-retention"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: framework.ControllerServiceAccount,
			Namespace: framework.ControllerNamespace}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	restore := e.PatchController(t, func(spec *corev1.PodSpec) {
		framework.SetArg(spec, "audit-retention", "true")
		framework.SetArg(spec, "audit-retention-max-per-pipeline", "3")
		// Age is measured from metadata.creationTimestamp, which the API
		// server sets: the records here are new, so only the count cap
		// deletes (the age limit is covered by the unit tests).
		framework.SetArg(spec, "audit-retention-max-age", "24h")
		framework.SetArg(spec, "audit-retention-interval", "5s")
	})
	defer restore()

	want := []string{"api-new", "api-old", "web-3", "web-4", "web-5"}
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
