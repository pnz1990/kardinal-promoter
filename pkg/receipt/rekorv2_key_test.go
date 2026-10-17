// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	rekortilespb "github.com/sigstore/rekor-tiles/v2/pkg/generated/protobuf"
	rekornote "github.com/sigstore/rekor-tiles/v2/pkg/note"
	"github.com/sigstore/rekor-tiles/v2/pkg/types/hashedrekord"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	flog "github.com/transparency-dev/formats/log"
	"github.com/transparency-dev/merkle/rfc6962"
	"golang.org/x/mod/sumdb/note"
	"google.golang.org/protobuf/encoding/protojson"
)

// trustedLog is trusted material holding one Rekor v2 log (and nothing
// else: a key receipt needs no CA).
type trustedLog struct {
	root.BaseTrustedMaterial
	logs map[string]*root.TransparencyLog
}

func (t *trustedLog) RekorLogs() map[string]*root.TransparencyLog { return t.logs }

// fakeRekorV2 is a Rekor v2 (rekor-tiles) log of one entry per request, as
// the real log builds it (hashedrekord.ToLogEntry, canonicalized, a signed
// checkpoint of a one-leaf tree), and trusted material that holds it.
func fakeRekorV2(t *testing.T) (*httptest.Server, root.TrustedMaterial) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(key.Public())
	require.NoError(t, err)
	id := sha256.Sum256(der)
	sv, err := signature.LoadECDSASignerVerifier(key, crypto.SHA256)
	require.NoError(t, err)
	algs, err := signature.NewAlgorithmRegistryConfig([]protocommon.PublicKeyDetails{protocommon.PublicKeyDetails_PKIX_ECDSA_P256_SHA_256})
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req rekortilespb.CreateEntryRequest
		if err := protojson.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		entry, err := hashedrekord.ToLogEntry(req.GetHashedRekordRequestV002(), algs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		serialized, _ := protojson.Marshal(entry)
		canon, err := jsoncanonicalizer.Transform(serialized)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		leaf := rfc6962.DefaultHasher.HashLeaf(canon)
		u, _ := url.Parse("http://" + r.Host)
		signer, err := rekornote.NewNoteSigner(context.Background(), u.Hostname(), sv)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cp, err := note.Sign(&note.Note{Text: string(flog.Checkpoint{Origin: u.Hostname(), Size: 1, Hash: leaf}.Marshal())}, signer)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, _ := protojson.Marshal(&protorekor.TransparencyLogEntry{LogIndex: 0, LogId: &protocommon.LogId{KeyId: id[:]},
			KindVersion: &protorekor.KindVersion{Kind: "hashedrekord", Version: "0.0.2"},
			InclusionProof: &protorekor.InclusionProof{LogIndex: 0, TreeSize: 1, RootHash: leaf, Hashes: [][]byte{},
				Checkpoint: &protorekor.Checkpoint{Envelope: string(cp)}},
			CanonicalizedBody: canon})
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	trusted := &trustedLog{logs: map[string]*root.TransparencyLog{
		hexID(id[:]): {BaseURL: srv.URL, ID: id[:], HashFunc: crypto.SHA256, PublicKey: key.Public(),
			SignatureHashFunc: crypto.SHA256, ValidityPeriodStart: time.Now().Add(-time.Hour)},
	}}
	return srv, trusted
}

func hexID(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}

// TestSign_KeyOnRekorV2Verifies (QA-2 #2120): a receipt signed with a key
// and logged in a Rekor v2 log verifies with the key and a trusted root
// holding the log, and is reported logged: a key needs no proof of time, so
// the entry's missing integrated time is no reason to refuse it. A root
// without that log refuses the entry.
//
// Covers RECEIPT-SIGN-07.
func TestSign_KeyOnRekorV2Verifies(t *testing.T) {
	srv, trusted := fakeRekorV2(t)
	s, pub := keySigner(t)
	s.RekorURL, s.RekorVersion = srv.URL, 2
	raw, _, err := Build(input("image"))
	require.NoError(t, err)
	signed, err := s.Sign(context.Background(), raw)
	require.NoError(t, err)
	require.NotNil(t, signed.LogIndex)

	v, err := Verify(signed.Bundle, VerifyOptions{Key: pub, TrustedRoot: trusted, Statement: raw})
	require.NoError(t, err)
	assert.True(t, v.Logged, "the Rekor v2 entry was verified")

	_, other := fakeRekorV2(t)
	_, err = Verify(signed.Bundle, VerifyOptions{Key: pub, TrustedRoot: other})
	require.Error(t, err, "a root without the log refuses its entry")
}
