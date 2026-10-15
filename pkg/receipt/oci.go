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
	"fmt"
	"io"
	"net/http"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// OCI referrer media type and annotation.
const (
	// BundleMediaType is the Sigstore bundle media type, the referrer's
	// artifact type.
	BundleMediaType = "application/vnd.dev.sigstore.bundle.v0.3+json"
	// PredicateTypeAnnotation names the statement's predicate on the
	// referrer manifest, so a reader of image signatures can skip receipts
	// before it fetches them (ImageVerification does).
	PredicateTypeAnnotation = "dev.sigstore.bundle.predicateType"
	// maxBundleBytes bounds a receipt blob read from a registry.
	maxBundleBytes = 2 << 20
	// maxReferrers bounds the receipt referrers read per image.
	maxReferrers = 64
)

// Registry pushes and reads receipt referrers.
type Registry struct {
	// Transport carries every request; nil is remote.DefaultTransport.
	Transport http.RoundTripper
	// Keychain authenticates; nil is anonymous.
	Keychain authn.Keychain
	// Insecure allows plain HTTP to the registry (an in-cluster registry).
	Insecure bool
}

func (r *Registry) options(ctx context.Context) []remote.Option {
	opts := []remote.Option{remote.WithContext(ctx)}
	if r.Transport != nil {
		opts = append(opts, remote.WithTransport(r.Transport))
	}
	if r.Keychain != nil {
		opts = append(opts, remote.WithAuthFromKeychain(r.Keychain))
	}
	return opts
}

func (r *Registry) nameOptions() []name.Option {
	if r.Insecure {
		return []name.Option{name.Insecure}
	}
	return nil
}

// PushReferrer pushes bundleJSON as a referrer of image@digest into
// repository (the image's own, or another one, as cosign's
// COSIGN_REPOSITORY) and returns the referrer, repository@sha256:... The
// push is content-addressed: pushing the same receipt again writes the same
// manifest.
//
// The subject descriptor is read from repository, else from the image's
// own repository. With another repository and neither readable (the image
// is in a registry the controller cannot reach), the descriptor names the
// digest as an OCI image manifest of unknown size: a referrer is found by
// its subject's digest alone.
func (r *Registry) PushReferrer(ctx context.Context, repository, image, digest string, bundleJSON []byte) (string, error) {
	opts := r.options(ctx)
	subject, err := r.subject(repository, image, digest, opts)
	if err != nil {
		return "", err
	}
	desc := subject
	layer := static.NewLayer(bundleJSON, types.MediaType(BundleMediaType))
	img, err := mutate.Append(empty.Image, mutate.Addendum{Layer: layer})
	if err != nil {
		return "", fmt.Errorf("build referrer: %w", err)
	}
	img = mutate.MediaType(img, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, types.MediaType(BundleMediaType))
	// Annotations before Subject: mutate.Annotations rebuilds the image
	// from its manifest and would drop a subject set before it.
	img = mutate.Annotations(img, map[string]string{PredicateTypeAnnotation: PredicateVSA}).(v1.Image)
	img = mutate.Subject(img, desc).(v1.Image)
	d, err := img.Digest()
	if err != nil {
		return "", fmt.Errorf("referrer digest: %w", err)
	}
	ref, err := name.NewDigest(repository+"@"+d.String(), r.nameOptions()...)
	if err != nil {
		return "", fmt.Errorf("referrer %s: %w", d, err)
	}
	if err := remote.Write(ref, img, opts...); err != nil {
		return "", fmt.Errorf("push referrer to %s: %w", repository, err)
	}
	return ref.String(), nil
}

// subject is the descriptor of image@digest: read from repository, else
// from image's repository, else (repository is not image's) a descriptor
// of the digest alone.
func (r *Registry) subject(repository, image, digest string, opts []remote.Option) (v1.Descriptor, error) {
	var firstErr error
	for _, repo := range []string{repository, image} {
		ref, err := name.NewDigest(repo+"@"+digest, r.nameOptions()...)
		if err != nil {
			return v1.Descriptor{}, fmt.Errorf("subject %s@%s: %w", repo, digest, err)
		}
		desc, err := remote.Head(ref, opts...)
		if err == nil {
			return v1.Descriptor{MediaType: desc.MediaType, Digest: desc.Digest, Size: desc.Size}, nil
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("read subject %s: %w", ref, err)
		}
		if repository == image {
			break
		}
	}
	if repository == image {
		return v1.Descriptor{}, firstErr
	}
	h, err := v1.NewHash(digest)
	if err != nil {
		return v1.Descriptor{}, fmt.Errorf("subject digest %s: %w", digest, err)
	}
	return v1.Descriptor{MediaType: types.OCIManifestSchema1, Digest: h}, nil
}

// Referrers returns the receipt bundles that refer to repository@digest:
// referrers of the Sigstore bundle artifact type annotated with the VSA
// predicate.
func (r *Registry) Referrers(ctx context.Context, repository, digest string) ([][]byte, error) {
	subject, err := name.NewDigest(repository+"@"+digest, r.nameOptions()...)
	if err != nil {
		return nil, fmt.Errorf("image %s@%s: %w", repository, digest, err)
	}
	opts := r.options(ctx)
	idx, err := remote.Referrers(subject, opts...)
	if err != nil {
		return nil, fmt.Errorf("list referrers of %s: %w", subject, err)
	}
	m, err := idx.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("list referrers of %s: %w", subject, err)
	}
	var out [][]byte
	for _, d := range m.Manifests {
		if d.ArtifactType != BundleMediaType || d.Annotations[PredicateTypeAnnotation] != PredicateVSA {
			continue
		}
		if len(out) == maxReferrers {
			break
		}
		b, err := r.firstLayer(subject.Context().Digest(d.Digest.String()), opts)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (r *Registry) firstLayer(ref name.Digest, opts []remote.Option) ([]byte, error) {
	img, err := remote.Image(ref, opts...)
	if err != nil {
		return nil, fmt.Errorf("read referrer %s: %w", ref, err)
	}
	layers, err := img.Layers()
	if err != nil || len(layers) == 0 {
		return nil, fmt.Errorf("read referrer %s: no layer", ref)
	}
	rc, err := layers[0].Compressed()
	if err != nil {
		return nil, fmt.Errorf("read referrer %s: %w", ref, err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, maxBundleBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read referrer %s: %w", ref, err)
	}
	if len(b) > maxBundleBytes {
		return nil, fmt.Errorf("referrer %s is larger than %d bytes", ref, maxBundleBytes)
	}
	return b, nil
}
