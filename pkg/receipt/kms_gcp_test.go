// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

//go:build kms_gcp

package receipt

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"hash/crc32"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/wrapperspb"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
)

const gcpKey = "projects/acme/locations/global/keyRings/kardinal/cryptoKeys/receipts"

// fakeCloudKMS is the Cloud KMS gRPC API the signer calls, with an ECDSA
// P-256 key version.
type fakeCloudKMS struct {
	kmspb.UnimplementedKeyManagementServiceServer
	key   *ecdsa.PrivateKey
	signs atomic.Int32
}

func (f *fakeCloudKMS) GetCryptoKey(_ context.Context, r *kmspb.GetCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	return &kmspb.CryptoKey{Name: r.Name, Purpose: kmspb.CryptoKey_ASYMMETRIC_SIGN}, nil
}

func (f *fakeCloudKMS) GetCryptoKeyVersion(_ context.Context, r *kmspb.GetCryptoKeyVersionRequest) (*kmspb.CryptoKeyVersion, error) {
	return &kmspb.CryptoKeyVersion{Name: r.Name, State: kmspb.CryptoKeyVersion_ENABLED,
		Algorithm: kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256}, nil
}

func (f *fakeCloudKMS) GetPublicKey(_ context.Context, r *kmspb.GetPublicKeyRequest) (*kmspb.PublicKey, error) {
	der, err := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &kmspb.PublicKey{Name: r.Name, Algorithm: kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256,
		Pem: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}, nil
}

func (f *fakeCloudKMS) AsymmetricSign(_ context.Context, r *kmspb.AsymmetricSignRequest) (*kmspb.AsymmetricSignResponse, error) {
	f.signs.Add(1)
	sig, err := ecdsa.SignASN1(rand.Reader, f.key, r.GetDigest().GetSha256())
	if err != nil {
		return nil, err
	}
	return &kmspb.AsymmetricSignResponse{Name: r.Name, Signature: sig, VerifiedDigestCrc32C: r.DigestCrc32C != nil,
		SignatureCrc32C: wrapperspb.Int64(int64(crc32.Checksum(sig, crc32.MakeTable(crc32.Castagnoli))))}, nil
}

// TestKMS_GCP (#1952): with the kms_gcp build tag, a kms receipt is signed
// by the Cloud KMS key version the reference names, and verifies with its
// public key. The API's connections go through gcpDial, the egress guard by
// default: it refuses the emulator's loopback address, so the test dials it
// directly.
//
// Covers RECEIPT-SIGN-06.
func TestKMS_GCP(t *testing.T) {
	t.Cleanup(CloseKMS)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	fake := &fakeCloudKMS{key: key}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	kmspb.RegisterKeyManagementServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	ref := "gcpkms://" + gcpKey + "/cryptoKeyVersions/1"
	require.Contains(t, KMSProviders(), "gcpkms://")
	raw, _, err := Build(input("image"))
	require.NoError(t, err)
	gcpTestOptions = []option.ClientOption{option.WithEndpoint(lis.Addr().String()), option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials()))}
	t.Cleanup(func() { gcpTestOptions = nil })

	s := &Signer{Mode: v1alpha1.ReceiptSigningKMS, KMSRef: ref, Timeout: 2 * time.Second}
	start := time.Now()
	_, err = s.Sign(context.Background(), raw)
	require.Error(t, err, "the egress guard refuses loopback")
	assert.ErrorIs(t, err, egress.ErrBlockedAddress, "the refusal is named")
	assert.Less(t, time.Since(start), 20*time.Second, "within the signer's timeout per call")
	assert.Zero(t, fake.signs.Load())

	gcpDial = (&net.Dialer{}).DialContext
	t.Cleanup(func() { gcpDial = egress.DialContext })
	signed, err := s.Sign(context.Background(), raw)
	require.NoError(t, err)
	assert.EqualValues(t, 1, fake.signs.Load(), "one AsymmetricSign call")
	assert.Equal(t, ref, signed.SignedBy.Identity)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	_, err = Verify(signed.Bundle, VerifyOptions{Key: pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})})
	require.NoError(t, err)
}

// fakeGCP serves fakeCloudKMS on loopback, dialled without the guard and
// without authentication, until the test ends.
func fakeGCP(t *testing.T) *fakeCloudKMS {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	fake := &fakeCloudKMS{key: key}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	kmspb.RegisterKeyManagementServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	gcpTestOptions = []option.ClientOption{option.WithEndpoint(lis.Addr().String()), option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials()))}
	gcpDial = (&net.Dialer{}).DialContext
	t.Cleanup(func() {
		CloseKMS()
		srv.Stop()
		gcpTestOptions, gcpDial = nil, egress.DialContext
	})
	return fake
}

// TestKMS_GCPClientReused (QA-3 #2111): 50 receipts signed with one Cloud
// KMS key build one client: the goroutines stay flat (they grew by about 8
// and a connection per receipt when each signature loaded the key), and
// CloseKMS closes its connections.
func TestKMS_GCPClientReused(t *testing.T) {
	fake := fakeGCP(t)
	s := &Signer{Mode: v1alpha1.ReceiptSigningKMS, KMSRef: "gcpkms://" + gcpKey + "/cryptoKeyVersions/1", Timeout: 2 * time.Second}
	raw, _, err := Build(input("image"))
	require.NoError(t, err)
	_, err = s.Sign(context.Background(), raw)
	require.NoError(t, err)
	before := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		_, err := s.Sign(context.Background(), raw)
		require.NoError(t, err)
	}
	assert.EqualValues(t, 51, fake.signs.Load())
	assert.LessOrEqual(t, runtime.NumGoroutine(), before+3, "one client for every signature")
	CloseKMS()
	_, err = s.Sign(context.Background(), raw)
	require.NoError(t, err, "a later signature loads the key again")
}

// TestKMS_GCPVersionPinned (QA-3 #2111): a Cloud KMS reference must pin a
// key version, at start and at signing: without one, a new version would
// change the signing key.
func TestKMS_GCPVersionPinned(t *testing.T) {
	for ref, ok := range map[string]bool{
		"gcpkms://" + gcpKey + "/cryptoKeyVersions/1": true,
		"gcpkms://" + gcpKey + "/versions/3":          true,
		"gcpkms://" + gcpKey:                          false,
		"gcpkms://" + gcpKey + "/cryptoKeyVersions/":  false,
	} {
		s := &Signer{Mode: v1alpha1.ReceiptSigningKMS, KMSRef: ref}
		err := s.Validate()
		if ok {
			assert.NoError(t, err, ref)
			continue
		}
		require.Error(t, err, ref)
		assert.Contains(t, err.Error(), "must pin a key version")
		_, err = s.Sign(context.Background(), []byte("{}"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must pin a key version", "refused at signing too")
	}
}

// TestKMS_GCPCancelledContextNotKept (QA-3 #2111): the cached key outlives
// the context of the signature that loaded it: once that context is
// cancelled (the receipt reconciler's per-receipt context), the next
// signature still signs.
func TestKMS_GCPCancelledContextNotKept(t *testing.T) {
	fake := fakeGCP(t)
	s := &Signer{Mode: v1alpha1.ReceiptSigningKMS, KMSRef: "gcpkms://" + gcpKey + "/cryptoKeyVersions/1", Timeout: 2 * time.Second}
	raw, _, err := Build(input("image"))
	require.NoError(t, err)
	first, cancel := context.WithCancel(context.Background())
	_, err = s.Sign(first, raw)
	require.NoError(t, err)
	cancel()
	_, err = s.Sign(context.Background(), raw)
	require.NoError(t, err, "a cancelled earlier context does not break later signatures")
	assert.EqualValues(t, 2, fake.signs.Load())
}
