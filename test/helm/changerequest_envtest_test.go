//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestChangeRequestsOnAPIServer (spec 23 §4): on a real kube-apiserver the
// ChangeRequest CRD refuses a provider without its settings, a link without
// spec.link, a non-https instance URL, a short-hand state outside the
// normalised set and a poll interval that is not a duration; it defaults
// mode, pollInterval, timeout, the ServiceNow change model and the auth
// scheme; a ServiceNow states map must map to the normalised states; and the
// AuditEvent CRD accepts the four ChangeRequest actions.
//
// Covers CHANGE-REQUEST-01.
func TestChangeRequestsOnAPIServer(t *testing.T) {
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(repoRoot(t), "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	require.NoError(t, v1alpha1.AddToScheme(sch))
	c, err := client.New(cfg, client.Options{Scheme: sch})
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}}))

	snow := func() *v1alpha1.ServiceNowChangeRequest {
		return &v1alpha1.ServiceNowChangeRequest{InstanceURL: "https://example.service-now.com",
			ChangeRequestAuth: v1alpha1.ChangeRequestAuth{CredentialsSecretRef: v1alpha1.SecretKeyRef{Name: "snow", Key: "token"}}}
	}
	n := 0
	cr := func(mut func(*v1alpha1.ChangeRequestSpec)) *v1alpha1.ChangeRequest {
		n++
		o := &v1alpha1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("cr-%d", n), Namespace: "team-a"},
			Spec: v1alpha1.ChangeRequestSpec{PerPromotion: true, Provider: "servicenow", ServiceNow: snow()}}
		mut(&o.Spec)
		return o
	}
	for _, tc := range []struct {
		name string
		mut  func(*v1alpha1.ChangeRequestSpec)
		want string
	}{
		{"servicenow, defaults", func(*v1alpha1.ChangeRequestSpec) {}, ""},
		{"jira", func(s *v1alpha1.ChangeRequestSpec) {
			s.Provider, s.ServiceNow = "jira", nil
			s.Jira = &v1alpha1.JiraChangeRequest{BaseURL: "https://example.atlassian.net", Project: "CHG",
				ChangeRequestAuth: v1alpha1.ChangeRequestAuth{CredentialsSecretRef: v1alpha1.SecretKeyRef{Name: "jira", Key: "token"}}}
		}, ""},
		{"link from an approval field", func(s *v1alpha1.ChangeRequestSpec) {
			s.Mode, s.Link = "link", &v1alpha1.ChangeRequestLink{From: "approvalField", Name: "ticketID"}
		}, ""},
		{"servicenow without its settings", func(s *v1alpha1.ChangeRequestSpec) { s.ServiceNow = nil }, "provider servicenow needs spec.servicenow"},
		{"jira without its settings", func(s *v1alpha1.ChangeRequestSpec) { s.Provider = "jira" }, "provider jira needs spec.jira"},
		{"link without spec.link", func(s *v1alpha1.ChangeRequestSpec) { s.Mode = "link" }, "mode link needs spec.link"},
		{"an http instance", func(s *v1alpha1.ChangeRequestSpec) { s.ServiceNow.InstanceURL = "http://example.service-now.com" }, "should match"},
		{"userinfo in the instance", func(s *v1alpha1.ChangeRequestSpec) { s.ServiceNow.InstanceURL = "https://a@b.service-now.com" }, "should match"},
		{"an unknown provider", func(s *v1alpha1.ChangeRequestSpec) { s.Provider = "remedy" }, "Unsupported value"},
		{"an unknown link source", func(s *v1alpha1.ChangeRequestSpec) {
			s.Mode, s.Link = "link", &v1alpha1.ChangeRequestLink{From: "comment", Name: "x"}
		}, "Unsupported value"},
		{"a poll interval that is not a duration", func(s *v1alpha1.ChangeRequestSpec) { s.PollInterval = "often" }, "should match"},
		{"timeout 0", func(s *v1alpha1.ChangeRequestSpec) { s.Timeout = "0" }, ""},
		{"bearer without an integration user", func(s *v1alpha1.ChangeRequestSpec) { s.ServiceNow.Scheme = "bearer" },
			"scheme bearer needs servicenow.integrationUser"},
		{"bearer with an integration user", func(s *v1alpha1.ChangeRequestSpec) {
			s.ServiceNow.Scheme, s.ServiceNow.IntegrationUser = "bearer", "svc.kardinal"
		}, ""},
		{"an integration user that is a query", func(s *v1alpha1.ChangeRequestSpec) { s.ServiceNow.IntegrationUser = "a^ORb" }, "should match"},
		{"a states map", func(s *v1alpha1.ChangeRequestSpec) {
			s.ServiceNow.States = map[string]string{"-3": "approved", "Implement": "closed"}
		}, ""},
		{"a states map to no state", func(s *v1alpha1.ChangeRequestSpec) { s.ServiceNow.States = map[string]string{"-3": "Scheduled"} },
			"a state maps to pending, approved, rejected, cancelled, closed or unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := cr(tc.mut)
			err := c.Create(ctx, o)
			if tc.want != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want)
				return
			}
			require.NoError(t, err)
			var got v1alpha1.ChangeRequest
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(o), &got))
			if tc.name == "servicenow, defaults" {
				assert.Equal(t, "open", got.Spec.Mode)
				assert.Equal(t, "1m", got.Spec.PollInterval)
				assert.Equal(t, "24h", got.Spec.Timeout)
				assert.Equal(t, "normal", got.Spec.ServiceNow.Type)
				assert.Equal(t, "basic", got.Spec.ServiceNow.Scheme)
			}
		})
	}

	t.Run("status state is the normalised set", func(t *testing.T) {
		o := cr(func(*v1alpha1.ChangeRequestSpec) {})
		require.NoError(t, c.Create(ctx, o))
		o.Status.State = "approved"
		require.NoError(t, c.Status().Update(ctx, o))
		o.Status.State = "Scheduled"
		err := c.Status().Update(ctx, o)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Unsupported value")
	})

	t.Run("AuditEvent accepts the ChangeRequest actions", func(t *testing.T) {
		for i, action := range []string{"ChangeRequestOpened", "ChangeRequestLinked", "ChangeRequestApproved", "ChangeRequestRejected"} {
			require.NoError(t, c.Create(ctx, &v1alpha1.AuditEvent{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("ae-%d", i), Namespace: "team-a"},
				Spec: v1alpha1.AuditEventSpec{Timestamp: metav1.Now(), PipelineName: "app", BundleName: "app-v1", Environment: "prod",
					Action: action, Outcome: "Success"}}), action)
		}
	})
}
