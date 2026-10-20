// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"flag"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestReceiptFlags: a configuration that cannot sign stops the controller
// at start, naming the flag: the public-good Rekor without
// --receipt-rekor-public (it publishes names and emails), key mode without
// its Secret, a KMS provider the binary was not built with, keyless without
// proof of time. The cluster identity is the kube-system Namespace UID
// unless --receipt-cluster-id is set.
//
// Covers RECEIPT-SIGN-03.
func TestReceiptFlags(t *testing.T) {
	parse := func(args ...string) *receiptFlags {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		f := bindReceiptFlags(fs)
		require.NoError(t, fs.Parse(args))
		return f
	}
	cases := map[string]struct {
		args []string
		want string
	}{
		"public rekor":       {[]string{"--receipt-rekor-url=https://rekor.sigstore.dev"}, "--receipt-rekor-public"},
		"key without secret": {[]string{"--receipt-signing-mode=key"}, "key Secret"},
		// azurekms is never built in, so no build tag makes this case reach
		// a real KMS.
		"kms not built":        {[]string{"--receipt-signing-mode=kms", "--receipt-kms-key=azurekms://k"}, "not built with the azurekms KMS provider"},
		"keyless without tsa":  {nil, "Rekor or a timestamp authority"},
		"rekor v2 without tsa": {[]string{"--receipt-rekor-url=https://rekor.example", "--receipt-rekor-version=2"}, "Rekor v2 needs a timestamp authority"},
		"bad rekor url":        {[]string{"--receipt-rekor-url=::"}, "is not a URL"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parse(tc.args...).signer(nil, "kardinal-system", nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	s, err := parse("--receipt-rekor-url=https://rekor.sigstore.dev", "--receipt-rekor-public").signer(nil, "kardinal-system", nil)
	require.NoError(t, err)
	assert.Equal(t, "keyless", s.Mode)
	s, err = parse("--receipt-signing-mode=key", "--receipt-key-secret=k").signer(nil, "kardinal-system", nil)
	require.NoError(t, err)
	assert.NotNil(t, s.Key)

	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	c := fake.NewClientBuilder().WithScheme(sch).WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "ks-uid"}}).Build()
	id, err := parse().clusterIdentity(context.Background(), c)
	require.NoError(t, err)
	assert.Equal(t, "ks-uid", id)
	id, err = parse("--receipt-cluster-id=prod-eu").clusterIdentity(context.Background(), c)
	require.NoError(t, err)
	assert.Equal(t, "prod-eu", id)
	_, err = parse().clusterIdentity(context.Background(), fake.NewClientBuilder().WithScheme(sch).Build())
	require.Error(t, err)
}
