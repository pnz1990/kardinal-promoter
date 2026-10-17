//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// The identity Fulcio certifies for the controller: its ServiceAccount, as
// the cluster's issuer (kind's default) names it.
const (
	keylessIssuer   = "https://kubernetes.default.svc.cluster.local"
	keylessIdentity = "https://kubernetes.io/namespaces/" + framework.ControllerNamespace +
		"/serviceaccounts/" + framework.ControllerServiceAccount
)

// keylessTrustedRoot is the private sigstore's trusted root
// (components/sigstore.sh, from its TUF repository).
func keylessTrustedRoot(t *testing.T) string {
	t.Helper()
	f := os.Getenv("KARDINAL_E2E_TRUSTED_ROOT")
	if f == "" {
		t.Skip("KARDINAL_E2E_TRUSTED_ROOT is not set: the receipts-keyless suite (hack/e2e/up.sh receipts-keyless) sets it")
	}
	return f
}

// TestReceiptsKeyless_SignedPromotion promotes a version by digest with
// keyless receipts against a private sigstore in the cluster (sigstore
// scaffolding: Fulcio trusting the cluster's service account issuer, Rekor
// v2, a timestamp authority). The receipt is signed with a short-lived
// Fulcio certificate for the controller's ServiceAccount, logged in Rekor v2
// and timestamped. kardinal verify-receipt passes with --identity, --issuer
// and the trusted root, from the cluster and from the receipt's file alone
// (the timestamp proves the signing time without asking Rekor), and fails
// for another identity or issuer.
//
// Covers RECEIPT-LIVE-05.
func TestReceiptsKeyless_SignedPromotion(t *testing.T) {
	root := keylessTrustedRoot(t)
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	rc := waitReceipt(t, e, a.ns, bundle+"-test", v1alpha1.ReceiptStored)

	require.NotNil(t, rc.Status.SignedBy)
	assert.Equal(t, v1alpha1.ReceiptSigningKeyless, rc.Status.SignedBy.Mode)
	assert.Equal(t, keylessIdentity, rc.Status.SignedBy.Identity)
	assert.Equal(t, keylessIssuer, rc.Status.SignedBy.Issuer)
	var logged bool
	for _, s := range rc.Status.Stored {
		if s.Sink == v1alpha1.ReceiptSinkRekor {
			logged = s.LogIndex != nil
		}
	}
	assert.True(t, logged, "logged in Rekor: %+v", rc.Status.Stored)
	var b struct {
		VerificationMaterial struct {
			TlogEntries []struct {
				KindVersion struct{ Kind, Version string } `json:"kindVersion"`
			} `json:"tlogEntries"`
			TimestampVerificationData struct {
				RFC3161Timestamps []json.RawMessage `json:"rfc3161Timestamps"`
			} `json:"timestampVerificationData"`
			Certificate *json.RawMessage `json:"certificate"`
		} `json:"verificationMaterial"`
	}
	require.NoError(t, json.Unmarshal([]byte(rc.Status.Bundle), &b))
	require.Len(t, b.VerificationMaterial.TlogEntries, 1)
	assert.Equal(t, "0.0.2", b.VerificationMaterial.TlogEntries[0].KindVersion.Version, "a Rekor v2 entry")
	assert.Len(t, b.VerificationMaterial.TimestampVerificationData.RFC3161Timestamps, 1, "timestamped")
	assert.NotNil(t, b.VerificationMaterial.Certificate, "a Fulcio certificate")

	ok := []string{"--identity", keylessIdentity, "--issuer", keylessIssuer, "--trusted-root", root}
	out := e.MustKardinal(t, a.ns, append([]string{"verify-receipt", pipelineName, "test"}, ok...)...)
	assert.Contains(t, out, "VERIFIED receipt "+bundle+"-test")

	file := filepath.Join(t.TempDir(), "receipt.sigstore.json")
	require.NoError(t, os.WriteFile(file, []byte(rc.Status.Bundle), 0o600))
	out = e.MustKardinal(t, "", append([]string{"verify-receipt", "--file", file, "--env", "test"}, ok...)...)
	assert.Contains(t, out, "VERIFIED receipt "+bundle+"-test")

	for name, args := range map[string][]string{
		"another identity": {"--identity", "https://kubernetes.io/namespaces/default/serviceaccounts/default",
			"--issuer", keylessIssuer, "--trusted-root", root},
		"another issuer": {"--identity", keylessIdentity, "--issuer", "https://accounts.google.com", "--trusted-root", root},
	} {
		_, err := e.Kardinal(t, "", append([]string{"verify-receipt", "--file", file}, args...)...)
		assert.Error(t, err, name)
	}
}
