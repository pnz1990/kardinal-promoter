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

// TestVerify_StatementPipelineMatchesResource (QA-3): a signed statement
// whose kardinalPromotion.pipeline is not its resourceUri's is refused,
// whatever the pins; a resourceUri that is not kardinal://<cluster>/
// namespaces/<ns>/pipelines/<p>/environments/<env> is refused too.
func TestVerify_StatementPipelineMatchesResource(t *testing.T) {
	s, pub := keySigner(t)
	raw, _, err := Build(input("image"))
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		edit func(*Statement)
		want string
	}{
		"another pipeline in the record": {func(st *Statement) { st.Predicate.Kardinal.Pipeline = "evil" }, `pipeline "evil" is not its resourceUri's`},
		"a resourceUri of another shape": {func(st *Statement) { st.Predicate.ResourceURI = "kardinal://9b1e/pipelines/checkout" }, "is not kardinal://<cluster>/namespaces/"},
	} {
		st, err := Parse(raw)
		require.NoError(t, err)
		tc.edit(st)
		b, err := json.Marshal(st)
		require.NoError(t, err)
		signed, err := s.Sign(context.Background(), b)
		require.NoError(t, err)
		_, err = Verify(signed.Bundle, VerifyOptions{Key: pub})
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), tc.want, name)
	}
}

// TestVerify_Pins (QA-3): each pin is checked in pkg/receipt on its own,
// not only through the CLI: a receipt verifies with every pin its
// resourceUri holds, fails with any one of them wrong, and with
// RequireCluster fails unless the cluster is pinned.
func TestVerify_Pins(t *testing.T) {
	s, pub := keySigner(t)
	raw, _, err := Build(input("image"))
	require.NoError(t, err)
	st, err := Parse(raw)
	require.NoError(t, err)
	r, err := ParseResourceURI(st.Predicate.ResourceURI)
	require.NoError(t, err)
	signed, err := s.Sign(context.Background(), raw)
	require.NoError(t, err)
	all := VerifyOptions{Key: pub, ClusterID: r.ClusterID, Namespace: r.Namespace, Pipeline: r.Pipeline, Environment: r.Environment, RequireCluster: true}
	_, err = Verify(signed.Bundle, all)
	require.NoError(t, err, "every pin right")

	for name, tc := range map[string]struct {
		edit func(*VerifyOptions)
		want string
	}{
		"cluster":          {func(o *VerifyOptions) { o.ClusterID = "other" }, "not cluster other"},
		"namespace":        {func(o *VerifyOptions) { o.Namespace = "other" }, "not namespace other"},
		"pipeline":         {func(o *VerifyOptions) { o.Pipeline = "other" }, "not pipeline other"},
		"environment":      {func(o *VerifyOptions) { o.Environment = "other" }, "not environment other"},
		"cluster unpinned": {func(o *VerifyOptions) { o.ClusterID = "" }, "pin it with --cluster " + r.ClusterID},
	} {
		t.Run(name, func(t *testing.T) {
			o := all
			tc.edit(&o)
			_, err := Verify(signed.Bundle, o)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	o := all
	o.ClusterID, o.RequireCluster = "", false
	_, err = Verify(signed.Bundle, o)
	require.NoError(t, err, "any cluster, when not required")
}

// TestVerify_Checks: Verify returns what it checked, one by one, and what
// it did not (an unpinned subject, an unchecked log entry): never just
// "verified".
//
// Covers RECEIPT-VERIFY-05.
func TestVerify_Checks(t *testing.T) {
	s, pub := keySigner(t)
	raw, _, err := Build(input("image"))
	require.NoError(t, err)
	st, err := Parse(raw)
	require.NoError(t, err)
	r, err := ParseResourceURI(st.Predicate.ResourceURI)
	require.NoError(t, err)
	signed, err := s.Sign(context.Background(), raw)
	require.NoError(t, err)
	byName := func(cs []Check) map[string]Check {
		m := map[string]Check{}
		for _, c := range cs {
			m[c.Name] = c
		}
		return m
	}

	v, err := Verify(signed.Bundle, VerifyOptions{Key: pub})
	require.NoError(t, err)
	c := byName(v.Checks)
	assert.True(t, c["signature"].Checked)
	assert.Contains(t, c["signature"].Detail, "key sha256:")
	assert.Equal(t, Check{Name: "transparency log", Detail: "no entry"}, c["transparency log"])
	assert.False(t, c["subject digest"].Checked)
	assert.False(t, c["resource"].Checked)
	assert.False(t, c["levels"].Checked)
	_, ok := c["statement"]
	assert.False(t, ok, "no statement given, none compared")

	v, err = Verify(signed.Bundle, VerifyOptions{Key: pub, Statement: raw, Subject: "sha256:" + st.Subject[0].Digest["sha256"],
		ClusterID: r.ClusterID, Namespace: r.Namespace, Pipeline: r.Pipeline, Environment: r.Environment, Require: []string{"HEALTHY"}})
	require.NoError(t, err)
	c = byName(v.Checks)
	for _, name := range []string{"signature", "statement", "subject digest", "resource", "levels"} {
		assert.True(t, c[name].Checked, name)
	}
	assert.Contains(t, c["resource"].Detail, "(cluster, namespace, pipeline, environment pinned)")
	assert.Equal(t, "HEALTHY as required", c["levels"].Detail)

	v, err = Verify(withForgedLogEntry(t, signed.Bundle), VerifyOptions{Key: pub})
	require.NoError(t, err)
	assert.Equal(t, Check{Name: "transparency log", Detail: "entry not checked (give --trusted-root)"}, byName(v.Checks)["transparency log"])
}
