// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification_test

import (
	"testing"

	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	iv "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification/signtest"
)

// TestAttestations_PrivateTrustedRoot (the evidence-airgap suite, #2062): a
// keyless attestation from a private Sigstore (signtest.KeylessAttest) is
// verified against the authority's trustedRootRef alone, with no TUF fetch;
// another Sigstore's attestation, or another workflow's, is not.
func TestAttestations_PrivateTrustedRoot(t *testing.T) {
	const issuer = "https://token.actions.githubusercontent.com"
	const identity = "https://github.com/myorg/app/.github/workflows/release.yml@refs/heads/main"
	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	other, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	root, err := signtest.TrustedRootJSON(vs)
	require.NoError(t, err)
	k := newKeys(t)
	secrets := k.secrets(t)
	rootSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sigstore", Namespace: ns,
		Labels: map[string]string{iv.LabelReferenceable: "true"}}, Data: map[string][]byte{"trusted_root.json": root}}
	policy := v1alpha1.AttestationPolicy{Name: "provenance", PredicateType: iv.PredicateSLSAProvenance, Authorities: []string{"ci"}}
	newPolicyIV := func() *v1alpha1.ImageVerification {
		v := attestIV("", policy)
		v.Spec.Policy.Authorities = append(v.Spec.Policy.Authorities, v1alpha1.SignatureAuthority{Name: "ci",
			Keyless: &v1alpha1.KeylessAuthority{Issuer: issuer, Subject: identity,
				TrustedRootRef: &v1alpha1.SecretKeyName{Name: "sigstore", Key: "trusted_root.json"}}})
		return v
	}
	cases := []struct {
		name     string
		vs       *ca.VirtualSigstore
		identity string
		verified bool
	}{
		{"the private Sigstore's", vs, identity, true},
		{"another Sigstore's", other, identity, false},
		{"another workflow's", vs, "https://github.com/evil/app/.github/workflows/x.yml@refs/heads/main", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := signtest.KeylessAttest(tc.vs, tc.identity, issuer, repo, digestA, iv.PredicateSLSAProvenance, provenance("https://builder.example"))
			require.NoError(t, err)
			h := newHarness(t, nil, newPolicyIV(), secrets[0], secrets[1], rootSecret)
			h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: [][]byte{b}}
			got, _ := h.reconcile()
			r := resultOf(t, got, "provenance")
			assert.Equal(t, tc.verified, r.Verified, r.Message)
			if tc.verified {
				assert.Equal(t, "ci", r.Authority)
				assert.Equal(t, identity, r.Signer)
			}
		})
	}
}
