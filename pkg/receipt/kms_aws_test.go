// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

//go:build kms_aws

package receipt

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// fakeAWSKMS answers the KMS JSON API calls the signer makes (DescribeKey,
// GetPublicKey, Sign) with an ECDSA P-256 key, and counts Sign calls.
func fakeAWSKMS(t *testing.T) (*httptest.Server, *ecdsa.PrivateKey, *atomic.Int32) {
	srv, key, signs, _ := fakeAWSKMSAuth(t)
	return srv, key, signs
}

// fakeAWSKMSAuth is fakeAWSKMS that also returns the Authorization header of
// the last Sign call (its SigV4 scope names the region).
func fakeAWSKMSAuth(t *testing.T) (*httptest.Server, *ecdsa.PrivateKey, *atomic.Int32, *atomic.Value) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	var signs atomic.Int32
	var auth atomic.Value
	meta := map[string]interface{}{"KeyId": "kardinal-receipts", "KeySpec": "ECC_NIST_P256",
		"CustomerMasterKeySpec": "ECC_NIST_P256", "KeyUsage": "SIGN_VERIFY", "SigningAlgorithms": []string{"ECDSA_SHA_256"},
		"Enabled": true, "KeyState": "Enabled"}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in map[string]string
		_ = json.Unmarshal(body, &in)
		var out interface{}
		switch r.Header.Get("X-Amz-Target") {
		case "TrentService.DescribeKey":
			out = map[string]interface{}{"KeyMetadata": meta}
		case "TrentService.GetPublicKey":
			out = map[string]interface{}{"KeyId": "kardinal-receipts", "PublicKey": base64.StdEncoding.EncodeToString(der),
				"KeySpec": "ECC_NIST_P256", "KeyUsage": "SIGN_VERIFY", "SigningAlgorithms": []string{"ECDSA_SHA_256"}}
		case "TrentService.Sign":
			signs.Add(1)
			auth.Store(r.Header.Get("Authorization"))
			digest, err := base64.StdEncoding.DecodeString(in["Message"])
			require.NoError(t, err)
			assert.Equal(t, "DIGEST", in["MessageType"])
			sig, err := ecdsa.SignASN1(rand.Reader, key, digest)
			require.NoError(t, err)
			out = map[string]interface{}{"KeyId": "kardinal-receipts", "Signature": base64.StdEncoding.EncodeToString(sig),
				"SigningAlgorithm": "ECDSA_SHA_256"}
		default:
			t.Errorf("unexpected KMS call %q", r.Header.Get("X-Amz-Target"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv, key, &signs, &auth
}

// TestKMS_AWS (#1952): with the kms_aws build tag, a kms receipt is signed
// by the AWS KMS key the reference names, through the transport the
// controller gives it, and verifies with the key's public key; the receipt
// names the key reference.
//
// Covers RECEIPT-SIGN-06.
// awsTestEnv gives the AWS SDK the test's static credentials only: never a
// profile, a role or the host's credentials.
func awsTestEnv(t *testing.T) {
	for _, v := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_SESSION_TOKEN", "AWS_ROLE_ARN",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_TLS_INSECURE_SKIP_VERIFY"} {
		t.Setenv(v, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
}

func TestKMS_AWS(t *testing.T) {
	awsTestEnv(t)
	srv, key, signs := fakeAWSKMS(t)
	ref := "awskms://" + strings.TrimPrefix(srv.URL, "https://") + "/1234abcd-12ab-34cd-56ef-1234567890ab"
	require.Contains(t, KMSProviders(), "awskms://")
	s := &Signer{Mode: v1alpha1.ReceiptSigningKMS, KMSRef: ref, Transport: srv.Client().Transport}
	require.NoError(t, s.Validate())
	raw, _, err := Build(input("image"))
	require.NoError(t, err)
	signed, err := s.Sign(context.Background(), raw)
	require.NoError(t, err)
	assert.EqualValues(t, 1, signs.Load(), "one KMS Sign call")
	assert.Equal(t, v1alpha1.ReceiptSigningKMS, signed.SignedBy.Mode)
	assert.Equal(t, ref, signed.SignedBy.Identity)

	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	pub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	_, err = Verify(signed.Bundle, VerifyOptions{Key: pub})
	require.NoError(t, err)
}

// TestKMS_AWSTransport: the SDK's credential requests (instance metadata,
// ECS, EKS Pod Identity) go directly; the KMS and STS calls go through the
// egress transport.
func TestKMS_AWSTransport(t *testing.T) {
	var egressed, direct []string
	rt := awsTransport{
		egress:      roundTrip(func(r *http.Request) { egressed = append(egressed, r.URL.Host) }),
		credentials: roundTrip(func(r *http.Request) { direct = append(direct, r.URL.Host) }),
	}
	for _, u := range []string{"http://169.254.169.254/latest/api/token", "http://169.254.170.23/v1/credentials",
		"http://[fd00:ec2::23]/v1/credentials", "https://kms.us-east-1.amazonaws.com/", "https://sts.amazonaws.com/",
		"http://169.254.169.253/"} {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		require.NoError(t, err)
		_, _ = rt.RoundTrip(req)
	}
	assert.Equal(t, []string{"169.254.169.254", "169.254.170.23", "[fd00:ec2::23]"}, direct)
	assert.Equal(t, []string{"kms.us-east-1.amazonaws.com", "sts.amazonaws.com", "169.254.169.253"}, egressed,
		"any other address, link-local or not, is the egress guard's to judge")
}

type roundTrip func(*http.Request)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	f(r)
	return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Request: r}, nil
}

// TestKMS_AWSRegionAndInsecure: an ARN reference signs in the ARN's region,
// whatever AWS_REGION says; AWS_TLS_INSECURE_SKIP_VERIFY=1, with which
// sigstore would replace the guarded client, is refused.
func TestKMS_AWSRegionAndInsecure(t *testing.T) {
	awsTestEnv(t)
	srv, _, _, auth := fakeAWSKMSAuth(t)
	host := strings.TrimPrefix(srv.URL, "https://")
	ref := "awskms://" + host + "/arn:aws:kms:eu-west-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	raw, _, err := Build(input("image"))
	require.NoError(t, err)
	s := &Signer{Mode: v1alpha1.ReceiptSigningKMS, KMSRef: ref, Transport: srv.Client().Transport}
	_, err = s.Sign(context.Background(), raw)
	require.NoError(t, err)
	assert.Contains(t, auth.Load(), "/eu-west-1/kms/", "signed for the ARN's region")

	t.Setenv("AWS_TLS_INSECURE_SKIP_VERIFY", "1")
	_, err = s.Sign(context.Background(), raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AWS_TLS_INSECURE_SKIP_VERIFY")
}
