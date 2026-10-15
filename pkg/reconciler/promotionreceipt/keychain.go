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

package promotionreceipt

import (
	"context"
	"crypto"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification"
)

// loadKeychain reads the push credentials of the OCI sink: a Secret the
// cluster admin names in receipts.sinks.oci.secretRef, in the controller's
// namespace, never a tenant's pull Secret.
func loadKeychain(ctx context.Context, c ctrl.Reader, ns, name string) (authn.Keychain, error) {
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
		return nil, fmt.Errorf("read the OCI sink Secret %s/%s: %w", ns, name, err)
	}
	raw, ok := s.Data[corev1.DockerConfigJsonKey]
	if !ok {
		return nil, fmt.Errorf("the OCI sink Secret %s/%s has no %s", ns, name, corev1.DockerConfigJsonKey)
	}
	kc, err := imageverification.DockerConfigKeychain(raw)
	if err != nil {
		return nil, fmt.Errorf("the OCI sink Secret %s/%s: %w", ns, name, err)
	}
	return kc, nil
}

// SecretKey is the KeySource of key mode: an ECDSA P-256 PEM key in a
// Secret of the controller's namespace, read on every signature, so a
// rotated key signs the next receipt.
func SecretKey(c ctrl.Reader, ns, name, key string) receipt.KeySource {
	return func(ctx context.Context) (crypto.Signer, string, error) {
		var s corev1.Secret
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
			return nil, "", fmt.Errorf("read the receipt key Secret %s/%s: %w", ns, name, err)
		}
		raw, ok := s.Data[key]
		if !ok {
			return nil, "", fmt.Errorf("the receipt key Secret %s/%s has no key %q", ns, name, key)
		}
		k, err := receipt.ParsePrivateKey(raw)
		if err != nil {
			return nil, "", fmt.Errorf("the receipt key Secret %s/%s: %w", ns, name, err)
		}
		return k, ns + "/" + name, nil
	}
}
