// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withForgedLogEntry is bundle with a transparency log entry nobody logged:
// a well-formed inclusion proof for a body of its own, signed by no log.
func withForgedLogEntry(t *testing.T, bundle []byte) []byte {
	t.Helper()
	var b map[string]interface{}
	require.NoError(t, json.Unmarshal(bundle, &b))
	vm := b["verificationMaterial"].(map[string]interface{})
	h := strings.Repeat("ab", 32)
	body := base64.StdEncoding.EncodeToString([]byte(`{"apiVersion":"0.0.1","kind":"dsse","spec":{` +
		`"envelopeHash":{"algorithm":"sha256","value":"` + h + `"},"payloadHash":{"algorithm":"sha256","value":"` + h + `"},` +
		`"signatures":[{"signature":"AAAA","verifier":"AAAA"}]}}`))
	hash := base64.StdEncoding.EncodeToString(make([]byte, 32))
	vm["tlogEntries"] = []interface{}{map[string]interface{}{
		"logIndex": "42", "logId": map[string]interface{}{"keyId": hash},
		"kindVersion":    map[string]interface{}{"kind": "dsse", "version": "0.0.1"},
		"integratedTime": "1760000000", "canonicalizedBody": body,
		"inclusionPromise": map[string]interface{}{"signedEntryTimestamp": hash},
		"inclusionProof": map[string]interface{}{"logIndex": "42", "rootHash": hash, "treeSize": "43", "hashes": []interface{}{hash},
			"checkpoint": map[string]interface{}{"envelope": "rekor.example\n43\n" + hash + "\n\n— rekor AAAA\n"}},
	}}
	out, err := json.Marshal(b)
	require.NoError(t, err)
	return out
}

// TestVerify_LogEntryClaimedOnlyWhenChecked (QA-3 B3): a key receipt whose
// bundle carries a transparency log entry is reported as logged only when
// the entry was verified against a trusted root. Without one the signature
// still verifies, and the entry is reported as not checked; with one, a
// forged entry fails the verification.
//
// Covers RECEIPT-VERIFY-03.
func TestVerify_LogEntryClaimedOnlyWhenChecked(t *testing.T) {
	s, pub := keySigner(t)
	raw, _, err := Build(input("image"))
	require.NoError(t, err)
	signed, err := s.Sign(context.Background(), raw)
	require.NoError(t, err)
	v, err := Verify(signed.Bundle, VerifyOptions{Key: pub})
	require.NoError(t, err)
	assert.False(t, v.Logged)
	assert.False(t, v.LogNotChecked, "no entry, nothing to check")

	forged := withForgedLogEntry(t, signed.Bundle)
	v, err = Verify(forged, VerifyOptions{Key: pub})
	require.NoError(t, err, "the signature is the key's")
	assert.False(t, v.Logged, "never claimed without checking the entry")
	assert.True(t, v.LogNotChecked)

	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	_, err = Verify(forged, VerifyOptions{Key: pub, TrustedRoot: vs})
	require.Error(t, err, "a forged entry fails against a trusted root")
}
