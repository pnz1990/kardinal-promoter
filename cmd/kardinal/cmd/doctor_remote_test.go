// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// fakeSSARServer answers SelfSubjectAccessReviews as a remote API server
// whose identity may do the verbs in allowed, and records the reviews.
func fakeSSARServer(t *testing.T, allowed ...string) (*httptest.Server, *[]authorizationv1.ResourceAttributes) {
	t.Helper()
	var seen []authorizationv1.ResourceAttributes
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" || r.Header.Get("Authorization") != "Bearer remote-token" {
			http.Error(w, `{"kind":"Status","message":"SECRET-BODY"}`, http.StatusForbidden)
			return
		}
		var review authorizationv1.SelfSubjectAccessReview
		require.NoError(t, json.NewDecoder(r.Body).Decode(&review))
		seen = append(seen, *review.Spec.ResourceAttributes)
		for _, v := range allowed {
			review.Status.Allowed = review.Status.Allowed || v == review.Spec.ResourceAttributes.Verb
		}
		review.APIVersion, review.Kind = "authorization.k8s.io/v1", "SelfSubjectAccessReview"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func doctorKubeconfigSecret(srv *httptest.Server, labelled bool) *corev1.Secret {
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	kc := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: hub
  cluster: {server: %q, certificate-authority-data: %s}
users:
- name: u
  user: {token: remote-token}
contexts:
- name: hub
  context: {cluster: hub, user: u}
current-context: hub
`, srv.URL, ca)
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "hub", Namespace: "team-a"}, Data: map[string][]byte{"kubeconfig": []byte(kc)}}
	if labelled {
		s.Labels = map[string]string{"kardinal.io/referenceable": "true"}
	}
	return s
}

// TestCheckRemoteHealthAccess (#1883): doctor --pipeline asks each remote
// cluster, as the kubeconfig's identity, whether it may get the object the
// health check names and patch it for health.refresh; a missing patch is a
// WARN (the refresh would be refused at promotion time), a missing get a
// FAIL, and the server's answers and address are not printed.
// Covers CLI-DOCTOR-02.
func TestCheckRemoteHealthAccess(t *testing.T) {
	old := doctorRemoteDial
	t.Cleanup(func() { doctorRemoteDial = old })
	doctorRemoteDial = (&net.Dialer{}).DialContext // the test server is on loopback, which the egress guard refuses

	off := false
	argo := func(refresh *bool) v1alpha1.EnvironmentSpec {
		return v1alpha1.EnvironmentSpec{Name: "prod", Health: v1alpha1.HealthConfig{Type: "argocd", Refresh: refresh,
			ArgoCD:              &v1alpha1.HealthTargetRef{Name: "spike-prod-cd", Namespace: "argocd"},
			KubeconfigSecretRef: &v1alpha1.KubeconfigSecretRef{Name: "hub"}}}
	}
	tests := []struct {
		name      string
		allowed   []string
		env       v1alpha1.EnvironmentSpec
		unlabeled bool
		noSecret  bool
		icon      string
		detail    string
		verbs     []string
	}{
		{name: "get and patch", allowed: []string{"get", "patch"}, env: argo(nil), icon: doctorPass,
			detail: "the kubeconfig's identity may get and patch Application argocd/spike-prod-cd", verbs: []string{"get", "patch"}},
		{name: "no patch", allowed: []string{"get"}, env: argo(nil), icon: doctorWarn,
			detail: "may get but not patch Application argocd/spike-prod-cd: health.refresh is refused there, so promotions wait for the GitOps tool's poll",
			verbs:  []string{"get", "patch"}},
		{name: "no patch needed with health.refresh: false", allowed: []string{"get"}, env: argo(&off), icon: doctorPass,
			detail: "may get Application argocd/spike-prod-cd", verbs: []string{"get"}},
		{name: "no get", env: argo(nil), icon: doctorFail, detail: "may not get or patch Application argocd/spike-prod-cd",
			verbs: []string{"get", "patch"}},
		{name: "the Secret is not referenceable", unlabeled: true, env: argo(nil), icon: doctorFail,
			detail: `does not have the label kardinal.io/referenceable: "true"`},
		{name: "the Secret cannot be read", noSecret: true, env: argo(nil), icon: doctorWarn,
			detail: "kubeconfig Secret team-a/hub could not be read"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, seen := fakeSSARServer(t, tc.allowed...)
			p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "spike", Namespace: "team-a"},
				Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
					{Name: "dev", Health: v1alpha1.HealthConfig{Type: "argocd"}}, // local: not checked here
					tc.env}}}
			var objs []sigs_client.Object
			if !tc.noSecret {
				objs = append(objs, doctorKubeconfigSecret(srv, !tc.unlabeled))
			}
			results := checkRemoteHealthAccess(context.Background(), doctorClient(objs...), p)
			require.Len(t, results, 1, "one result per remote environment")
			r := results[0]
			assert.Equal(t, "Remote health prod", r.label)
			assert.Equal(t, tc.icon, r.icon, r.detail)
			assert.Contains(t, r.detail, tc.detail)
			assert.NotContains(t, r.detail+r.hint, "SECRET-BODY")
			assert.NotContains(t, r.detail+r.hint, srv.URL)
			var verbs []string
			for _, a := range *seen {
				assert.Equal(t, authorizationv1.ResourceAttributes{Group: "argoproj.io", Resource: "applications",
					Namespace: "argocd", Name: "spike-prod-cd", Verb: a.Verb}, a)
				verbs = append(verbs, a.Verb)
			}
			assert.Equal(t, tc.verbs, verbs)
		})
	}
}

// TestCheckRemoteHealthAccess_EKS (#2008): an environment with
// kubeconfigSecretRef.eks is checked for its kubeconfig only (doctor cannot
// act as the controller's role): one without credentials, or none at all,
// is a WARN naming the cluster and role and what the role needs, one with a
// token (which the controller refuses with eks) a FAIL. No request reaches
// the cluster.
func TestCheckRemoteHealthAccess_EKS(t *testing.T) {
	srv, seen := fakeSSARServer(t)
	env := v1alpha1.EnvironmentSpec{Name: "prod", Health: v1alpha1.HealthConfig{Type: "resource",
		KubeconfigSecretRef: &v1alpha1.KubeconfigSecretRef{Name: "hub", EKS: &v1alpha1.EKSCredentials{
			ClusterName: "prod-eu", Region: "eu-west-1", RoleARN: "arn:aws:iam::111122223333:role/kardinal-health-team-a"}}}}
	p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "spike", Namespace: "team-a"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{env}}}
	withToken := doctorKubeconfigSecret(srv, true)
	noUser := withToken.DeepCopy()
	noUser.Data["kubeconfig"] = []byte(strings.Replace(string(withToken.Data["kubeconfig"]), "{token: remote-token}", "{}", 1))
	r := checkRemoteHealthAccess(context.Background(), doctorClient(noUser), p)[0]
	assert.Equal(t, doctorWarn, r.icon, r.detail)
	assert.Contains(t, r.detail, "EKS cluster prod-eu (eu-west-1) as arn:aws:iam::111122223333:role/kardinal-health-team-a")
	assert.Contains(t, r.hint, "kardinal-namespace=team-a")
	assert.Contains(t, r.hint, "allowedRoleARNsByNamespace.team-a")
	assert.Contains(t, r.hint, "eks:DescribeCluster")
	empty := noUser.DeepCopy()
	empty.Data = nil
	r = checkRemoteHealthAccess(context.Background(), doctorClient(empty), p)[0]
	assert.Equal(t, doctorWarn, r.icon, "with eks the kubeconfig is optional: %s", r.detail)
	r = checkRemoteHealthAccess(context.Background(), doctorClient(withToken), p)[0]
	assert.Equal(t, doctorFail, r.icon, r.detail)
	assert.Contains(t, r.detail, "remove users[].user's credentials")
	assert.Empty(t, *seen, "doctor does not reach the cluster")
}
