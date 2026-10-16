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

package receipt

import (
	"context"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegistry_PushReferrer: a receipt is pushed as a referrer of the
// image, annotated with the VSA predicate type, both on a registry with the
// referrers API and on one without it (the fallback tag); pushing it again
// writes the same manifest, and Referrers reads it back.
//
// Covers RECEIPT-OCI-01.
func TestRegistry_PushReferrer(t *testing.T) {
	for name, referrersAPI := range map[string]bool{"referrers API": true, "fallback tag": false} {
		t.Run(name, func(t *testing.T) {
			opts := []registry.Option{registry.Logger(log.New(io.Discard, "", 0))}
			if referrersAPI {
				opts = append(opts, registry.WithReferrersSupport(true))
			}
			srv := httptest.NewServer(registry.New(opts...))
			defer srv.Close()
			repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/checkout"
			img, err := random.Image(128, 1)
			require.NoError(t, err)
			require.NoError(t, remote.Write(mustRef(t, repo+":1.5.0"), img))
			d, err := img.Digest()
			require.NoError(t, err)

			s, pub := keySigner(t)
			in := input("image")
			in.Bundle.Spec.Images[0].Repository, in.Bundle.Spec.Images[0].Digest = repo, d.String()
			raw, _, err := Build(in)
			require.NoError(t, err)
			signed, err := s.Sign(context.Background(), raw)
			require.NoError(t, err)

			r := &Registry{Insecure: true}
			ref, err := r.PushReferrer(context.Background(), repo, repo, d.String(), signed.Bundle)
			require.NoError(t, err)
			again, err := r.PushReferrer(context.Background(), repo, repo, d.String(), signed.Bundle)
			require.NoError(t, err)
			assert.Equal(t, ref, again, "content-addressed")

			got, err := r.Referrers(context.Background(), repo, d.String())
			require.NoError(t, err)
			require.Len(t, got, 1)
			v, err := Verify(got[0], VerifyOptions{Key: pub, Subject: d.String(), Environment: "prod"})
			require.NoError(t, err)
			assert.Equal(t, "checkout-1-5-0-prod", v.Statement.Predicate.Kardinal.Receipt)

			idx, err := remote.Referrers(mustRef(t, repo+"@"+d.String()).Context().Digest(d.String()))
			require.NoError(t, err)
			m, err := idx.IndexManifest()
			require.NoError(t, err)
			require.Len(t, m.Manifests, 1)
			assert.Equal(t, PredicateVSA, m.Manifests[0].Annotations[PredicateTypeAnnotation])
			assert.Equal(t, "true", m.Manifests[0].Annotations[ReceiptAnnotation], "marked as a receipt")
			assert.Equal(t, BundleMediaType, m.Manifests[0].ArtifactType)

			// A builder's VSA (same artifact and predicate type, no receipt
			// annotation) is not read as a receipt.
			pushBuilderVSA(t, repo, d.String())
			got, err = r.Referrers(context.Background(), repo, d.String())
			require.NoError(t, err)
			assert.Len(t, got, 1, "the builder's VSA is skipped")
		})
	}
}

// pushBuilderVSA pushes a referrer as a build system's verification
// summary: a Sigstore bundle with the VSA predicate type, not a receipt.
func pushBuilderVSA(t *testing.T, repo, digest string) {
	t.Helper()
	subject, err := remote.Get(mustRef(t, repo+"@"+digest))
	require.NoError(t, err)
	img, err := mutate.Append(empty.Image, mutate.Addendum{Layer: static.NewLayer([]byte(`{}`), types.MediaType(BundleMediaType))})
	require.NoError(t, err)
	img = mutate.MediaType(img, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, types.MediaType(BundleMediaType))
	img = mutate.Annotations(img, map[string]string{PredicateTypeAnnotation: PredicateVSA}).(v1.Image)
	img = mutate.Subject(img, subject.Descriptor).(v1.Image)
	vd, err := img.Digest()
	require.NoError(t, err)
	require.NoError(t, remote.Write(mustRef(t, repo+"@"+vd.String()), img))
}

func mustRef(t *testing.T, s string) name.Reference {
	t.Helper()
	ref, err := name.ParseReference(s, name.Insecure)
	require.NoError(t, err)
	return ref
}

// TestRegistry_PushReferrerOtherRepository: with another repository, a
// receipt of an image the registry cannot reach is pushed there with a
// descriptor of the digest alone, and found by the digest.
//
// Covers RECEIPT-OCI-01.
func TestRegistry_PushReferrerOtherRepository(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.WithReferrersSupport(true), registry.Logger(log.New(io.Discard, "", 0))))
	defer srv.Close()
	repo := strings.TrimPrefix(srv.URL, "http://") + "/e2e/receipts"
	const image = "unreachable.invalid/acme/checkout"
	s, pub := keySigner(t)
	in := input("image")
	in.Bundle.Spec.Images[0].Repository = image
	raw, _, err := Build(in)
	require.NoError(t, err)
	signed, err := s.Sign(context.Background(), raw)
	require.NoError(t, err)
	r := &Registry{Insecure: true}
	ref, err := r.PushReferrer(context.Background(), repo, image, imageDigest, signed.Bundle)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(ref, repo+"@sha256:"))
	got, err := r.Referrers(context.Background(), repo, imageDigest)
	require.NoError(t, err)
	require.Len(t, got, 1)
	_, err = Verify(got[0], VerifyOptions{Key: pub, Subject: imageDigest})
	require.NoError(t, err)

	_, err = r.PushReferrer(context.Background(), image, image, imageDigest, signed.Bundle)
	require.Error(t, err, "its own repository unreachable: no push")
}
