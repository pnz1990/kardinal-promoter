// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func controllerPod(t *testing.T, docs []renderedDoc) corev1.PodSpec {
	t.Helper()
	deps := docsOfKind(docs, "Deployment")
	require.Len(t, deps, 1)
	var dep appsv1.Deployment
	decodeStrict(t, deps[0], &dep)
	return dep.Spec.Template.Spec
}

// TestChartReceipts: receipts are off by default (no flag, no token, no
// receipt RBAC). receipts.enabled with keyless signing passes the Fulcio URL
// and mounts a projected token of audience sigstore, separate from the API
// server token; key mode needs the key Secret; the sinks map to their
// flags; namespace mode may read the kube-system Namespace (the cluster
// identity), and only that one.
//
// Covers RECEIPT-CHART-01.
func TestChartReceipts(t *testing.T) {
	off := render(t, "kardinal-promoter")
	assert.NotContains(t, argValues(controllerContainer(t, off)), "receipts")
	for _, v := range controllerPod(t, off).Volumes {
		assert.NotEqual(t, "sigstore-token", v.Name)
	}
	ov := newRBACView(t, off)
	assert.False(t, ov.allowed(releaseNS, "kardinal-promoter", "team-a", "kardinal.io", "promotionreceipts", "create", ""))

	on := render(t, "kardinal-promoter", "--set", "receipts.enabled=true")
	args := argValues(controllerContainer(t, on))
	assert.Equal(t, "true", args["receipts"])
	assert.Equal(t, "keyless", args["receipt-signing-mode"])
	assert.Equal(t, "https://fulcio.sigstore.dev", args["receipt-fulcio-url"])
	assert.Equal(t, "/var/run/secrets/sigstore/token", args["receipt-token-file"])
	pod := controllerPod(t, on)
	var token *corev1.Volume
	for i := range pod.Volumes {
		if pod.Volumes[i].Name == "sigstore-token" {
			token = &pod.Volumes[i]
		}
	}
	require.NotNil(t, token)
	sat := token.Projected.Sources[0].ServiceAccountToken
	assert.Equal(t, "sigstore", sat.Audience)
	assert.Equal(t, int64(600), *sat.ExpirationSeconds)

	key := render(t, "kardinal-promoter", "--set", "receipts.enabled=true", "--set", "receipts.signing.mode=key",
		"--set", "receipts.signing.key.secretRef.name=receipt-key",
		"--set", "receipts.sinks.oci.enabled=true", "--set", "receipts.sinks.oci.secretRef.name=receipt-push",
		"--set", "receipts.sinks.rekor.url=https://rekor.internal", "--set", "receipts.signing.tsaURL=https://tsa.internal",
		"--set", "receipts.clusterID=prod-eu-1")
	args = argValues(controllerContainer(t, key))
	assert.Equal(t, "receipt-key", args["receipt-key-secret"])
	assert.Equal(t, "cosign.key", args["receipt-key-secret-key"])
	assert.Equal(t, "true", args["receipt-oci"])
	assert.Equal(t, "receipt-push", args["receipt-oci-secret"])
	assert.Equal(t, "https://rekor.internal", args["receipt-rekor-url"])
	assert.Equal(t, "https://tsa.internal", args["receipt-tsa-url"])
	assert.Equal(t, "prod-eu-1", args["receipt-cluster-id"])
	assert.NotContains(t, args, "receipt-fulcio-url")
	for _, v := range controllerPod(t, key).Volumes {
		assert.NotEqual(t, "sigstore-token", v.Name, "no Fulcio token for key mode")
	}

	out, err := helmTemplate(t, "kardinal-promoter", "--set", "receipts.enabled=true", "--set", "receipts.signing.mode=key")
	require.Error(t, err)
	assert.Contains(t, out, "receipts.signing.key.secretRef.name is required")

	// A release upgraded with --reuse-values from a chart without receipts.
	reused := render(t, "kardinal-promoter", "--set", "receipts=null")
	assert.NotContains(t, argValues(controllerContainer(t, reused)), "receipts")

	keep := newRBACView(t, render(t, "kardinal-promoter", "--set", "receipts.enabled=true", "--set", "receipts.retention.maxAge=8760h"))
	assert.True(t, keep.allowed(releaseNS, "kardinal-promoter", "team-a", "kardinal.io", "promotionreceipts", "delete", ""),
		"retention deletes past maxAge")
	assert.Equal(t, "8760h", argValues(controllerContainer(t, render(t, "kardinal-promoter", "--set", "receipts.enabled=true",
		"--set", "receipts.retention.maxAge=8760h")))["receipt-retention-max-age"])

	ns := newRBACView(t, render(t, "kardinal-promoter", "--set", "receipts.enabled=true", "--set", "controller.watchNamespace="+releaseNS))
	assert.True(t, ns.allowed(releaseNS, "kardinal-promoter", "", "", "namespaces", "get", "kube-system"))
	assert.False(t, ns.allowed(releaseNS, "kardinal-promoter", "", "", "namespaces", "get", "default"))
	assert.True(t, ns.allowed(releaseNS, "kardinal-promoter", releaseNS, "kardinal.io", "promotionreceipts", "create", ""))
	assert.False(t, ns.allowed(releaseNS, "kardinal-promoter", releaseNS, "kardinal.io", "promotionreceipts", "delete", ""))
}
