// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSign_RekorV2 (#2059): with RekorVersion 2 the signature is logged in
// a Rekor v2 (rekor-tiles) log as a hashedrekord entry, through the
// signer's own transport, and the returned entry is in the bundle (its log
// index in Signed.LogIndex). An error answer fails the signature.
//
// Covers RECEIPT-SIGN-07.
func TestSign_RekorV2(t *testing.T) {
	var posts, viaTransport atomic.Int32
	status := http.StatusCreated
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v2/log/entries", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		posts.Add(1)
		var req map[string]interface{}
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &req))
		h, ok := req["hashedRekordRequestV002"].(map[string]interface{})
		require.True(t, ok, "a DSSE receipt is logged as hashedrekord: %s", body)
		// The entry as the log canonicalizes it.
		canon, err := json.Marshal(map[string]interface{}{"apiVersion": "0.0.2", "kind": "hashedrekord",
			"spec": map[string]interface{}{"hashedRekordV002": map[string]interface{}{
				"data": map[string]interface{}{"algorithm": "SHA2_256", "digest": h["digest"]}, "signature": h["signature"]}}})
		require.NoError(t, err)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"logIndex":"7","logId":{"keyId":"AAAA"},"kindVersion":{"kind":"hashedrekord","version":"0.0.2"},`+
			`"integratedTime":"0","inclusionProof":{"logIndex":"7","rootHash":"AAAA","treeSize":"8","hashes":[],`+
			`"checkpoint":{"envelope":"rekor\n8\nAAAA\n"}},"canonicalizedBody":"`+base64.StdEncoding.EncodeToString(canon)+`"}`)
	}))
	defer srv.Close()
	s, _ := keySigner(t)
	s.RekorURL, s.RekorVersion = srv.URL, 2
	s.Transport = countingTransport{&viaTransport, http.DefaultTransport}
	raw, _, err := Build(input("image"))
	require.NoError(t, err)
	signed, err := s.Sign(context.Background(), raw)
	require.NoError(t, err)
	require.NotNil(t, signed.LogIndex)
	assert.EqualValues(t, 7, *signed.LogIndex)
	assert.EqualValues(t, 1, posts.Load())
	assert.EqualValues(t, 1, viaTransport.Load(), "through the signer's transport (the egress guard)")
	var b map[string]interface{}
	require.NoError(t, json.Unmarshal(signed.Bundle, &b))
	assert.Contains(t, string(signed.Bundle), `"tlogEntries"`)

	status = http.StatusTooManyRequests
	_, err = s.Sign(context.Background(), raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "429")

	s.RekorVersion = 3
	_, err = s.Sign(context.Background(), raw)
	require.Error(t, err)
}

type countingTransport struct {
	n  *atomic.Int32
	rt http.RoundTripper
}

func (c countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.rt.RoundTrip(r)
}
