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
	t.Parallel()
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

// TestChartReceiptsOCIPerNamespace (#2285): receipts.sinks.oci.perNamespace,
// namespaceRepositories and namespaceAnnotation reach the controller's
// flags; the defaults (off, no map, the annotation read) render none, so an
// upgrade changes nothing until an operator opts in, and so does a release
// reused from a chart without them. The schema refuses another mode, a
// namespace that is not a name and an entry with a separator in it.
//
// Covers RECEIPT-OCI-06.
func TestChartReceiptsOCIPerNamespace(t *testing.T) {
	t.Parallel()
	base := []string{"--set", "receipts.enabled=true", "--set", "receipts.signing.mode=key",
		"--set", "receipts.signing.key.secretRef.name=receipt-key", "--set", "receipts.sinks.oci.enabled=true"}
	off := argValues(controllerContainer(t, render(t, "kardinal-promoter", base...)))
	for _, flag := range []string{"receipt-oci-per-namespace", "receipt-oci-namespace-repositories", "receipt-oci-namespace-annotation"} {
		assert.NotContains(t, off, flag)
	}
	reused := argValues(controllerContainer(t, render(t, "kardinal-promoter", append(base,
		"--set", "receipts.sinks.oci.perNamespace=null", "--set", "receipts.sinks.oci.namespaceRepositories=null",
		"--set", "receipts.sinks.oci.namespaceAnnotation=null")...)))
	assert.NotContains(t, reused, "receipt-oci-per-namespace")
	assert.NotContains(t, reused, "receipt-oci-namespace-annotation")

	on := argValues(controllerContainer(t, render(t, "kardinal-promoter", append(base,
		"--set", "receipts.sinks.oci.perNamespace=required", "--set", "receipts.sinks.oci.namespaceAnnotation=false",
		"--set", "receipts.sinks.oci.namespaceRepositories.team-b={ghcr.io/team-b/}",
		"--set", "receipts.sinks.oci.namespaceRepositories.team-a={ghcr.io/team-a/,docker.io/team-a}")...)))
	assert.Equal(t, "required", on["receipt-oci-per-namespace"])
	assert.Equal(t, "team-a=ghcr.io/team-a/,docker.io/team-a;team-b=ghcr.io/team-b/;", on["receipt-oci-namespace-repositories"])
	assert.Equal(t, "false", on["receipt-oci-namespace-annotation"])

	for name, set := range map[string]string{
		"another mode":          "receipts.sinks.oci.perNamespace=strict",
		"a namespace not named": "receipts.sinks.oci.namespaceRepositories.Team_A={ghcr.io/a/}",
		"a separator":           "receipts.sinks.oci.namespaceRepositories.team-a={ghcr.io/a/;team-b=ghcr.io/}",
	} {
		out, err := helmTemplate(t, "kardinal-promoter", append(base, "--set", set)...)
		assert.Error(t, err, "%s: %s", name, out)
	}
}

// TestChartReceiptsRekorV2NeedsTSA (QA-2 #2120): keyless signing with Rekor
// v2 and no timestamp authority is refused by the values schema: a Rekor
// v2 entry does not prove the signing time, so the receipt could never
// verify once its short-lived certificate expired. With a TSA, with Rekor
// v1, or with key signing it renders.
func TestChartReceiptsRekorV2NeedsTSA(t *testing.T) {
	base := []string{"--set", "receipts.enabled=true", "--set", "receipts.signing.mode=keyless",
		"--set", "receipts.signing.keyless.fulcioURL=https://fulcio.example", "--set", "receipts.sinks.rekor.url=https://rekor.example"}
	out, err := helmTemplate(t, "kardinal-promoter", append(base, "--set", "receipts.sinks.rekor.version=2")...)
	require.Error(t, err, out)
	assert.Contains(t, out+err.Error(), "tsaURL")
	for name, extra := range map[string][]string{
		"with a TSA": {"--set", "receipts.sinks.rekor.version=2", "--set", "receipts.signing.tsaURL=https://tsa.example/api/v1/timestamp"},
		"rekor v1":   {"--set", "receipts.sinks.rekor.version=1"},
	} {
		_, err := helmTemplate(t, "kardinal-promoter", append(base, extra...)...)
		assert.NoError(t, err, name)
	}
}
