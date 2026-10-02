// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// EnvCertIssuer is the self-signed cert-manager ClusterIssuer
// hack/e2e/components/cert-manager.sh installs.
const EnvCertIssuer = "KARDINAL_E2E_CERT_ISSUER"

// CertificateGVR is cert-manager's Certificate.
var CertificateGVR = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}

// Certificate has cert-manager issue Certificate name in ns for dnsNames
// from the suite's ClusterIssuer and waits for its Secret (also name), with
// tls.crt, tls.key and ca.crt. The certificate is its own CA (isCA), so a
// client trusts it with --cacert ca.crt. It is deleted with the namespace.
func (e *Env) Certificate(t *testing.T, ns, name string, dnsNames ...string) *corev1.Secret {
	t.Helper()
	issuer := os.Getenv(EnvCertIssuer)
	if issuer == "" {
		t.Fatalf("%s is not set; run hack/e2e/up.sh chart", EnvCertIssuer)
	}
	names := make([]interface{}, len(dnsNames))
	for i, n := range dnsNames {
		names[i] = n
	}
	cert := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec": map[string]interface{}{
			"secretName": name,
			"isCA":       true,
			"commonName": name, // a DNS name can exceed the 64-byte CN limit
			"dnsNames":   names,
			"issuerRef":  map[string]interface{}{"name": issuer, "kind": "ClusterIssuer"},
		},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := e.Dynamic.Resource(CertificateGVR).Namespace(ns).Create(ctx, cert, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create Certificate %s/%s: %v", ns, name, err)
	}
	var s corev1.Secret
	Eventually(t, 2*time.Minute, "cert-manager issues Certificate "+name, func(ctx context.Context) (bool, string) {
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
			return false, err.Error()
		}
		return len(s.Data["tls.crt"]) > 0 && len(s.Data["tls.key"]) > 0 && len(s.Data["ca.crt"]) > 0, "Secret has no tls.crt, tls.key or ca.crt yet"
	})
	return &s
}
