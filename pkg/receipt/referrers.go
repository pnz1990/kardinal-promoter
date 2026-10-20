// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// MaxReferrerEntries is the most referrer descriptors of one image read,
// across the Referrers API's pages: past it the result says it is
// truncated, and a receipt that is not found among them is not "absent"
// but unknown.
const MaxReferrerEntries = 1024

// maxReferrerEntries is MaxReferrerEntries (tests lower it).
var maxReferrerEntries = MaxReferrerEntries

// maxReferrerPages bounds the pages of the Referrers API read for one
// image: 64, so a page holds 16 descriptors on average at the 1024 ceiling.
// A registry that pages past it (or links a page back to one already read)
// cannot be read whole: the result is an error, never a partial list.
const maxReferrerPages = 64

// ErrReferrersIncomplete is a referrers list that could not be read whole:
// too many pages, a page linked twice, a next page on another host. A
// receipt not found in it is unknown, so verification cannot conclude.
var ErrReferrersIncomplete = errors.New("cannot verify: the image's referrers cannot be read whole")

// maxIndexBytes bounds one page of the referrers index.
const maxIndexBytes = 4 << 20

// ResourceAnnotation holds a receipt referrer's resourceUri on its
// manifest, which the referrers index repeats: a reader looking for one
// environment's receipt skips the others before it fetches them. It is not
// signed, so it only narrows the search; the receipt read is verified.
const ResourceAnnotation = "kardinal.io/receipt-resource"

// Found are the receipts Referrers read for an image.
type Found struct {
	// Bundles are the matching receipts' Sigstore bundles, at most
	// maxReferrers.
	Bundles [][]byte
	// Entries counts the referrer descriptors read.
	Entries int
	// Truncated: the image has more referrers than MaxReferrerEntries, or
	// more matching receipts than maxReferrers. A receipt not in Bundles
	// may exist.
	Truncated bool
}

// Referrers reads the receipt referrers of repository@digest whose
// ResourceAnnotation match accepts (a referrer without the annotation, from
// an older controller, is always read): it follows the Referrers API's
// pages (Link: rel="next", on the same registry only), and falls back to
// the referrers tag schema for a registry without the API.
func (r *Registry) Referrers(ctx context.Context, repository, digest string, match func(resource string) bool) (Found, error) {
	subject, err := name.NewDigest(repository+"@"+digest, r.nameOptions()...)
	if err != nil {
		return Found{}, fmt.Errorf("image %s@%s: %w", repository, digest, err)
	}
	descs, truncated, err := r.referrerDescriptors(ctx, subject)
	if err != nil {
		return Found{}, fmt.Errorf("list referrers of %s: %w", subject, err)
	}
	out := Found{Entries: len(descs), Truncated: truncated}
	opts := r.options(ctx)
	for _, d := range descs {
		if d.ArtifactType != BundleMediaType || d.Annotations[PredicateTypeAnnotation] != PredicateVSA ||
			d.Annotations[ReceiptAnnotation] != "true" {
			continue
		}
		if res := d.Annotations[ResourceAnnotation]; res != "" && match != nil && !match(res) {
			continue
		}
		if len(out.Bundles) == maxReferrers {
			out.Truncated = true
			break
		}
		b, err := r.firstLayer(subject.Context().Digest(d.Digest.String()), opts)
		if err != nil {
			return Found{}, err
		}
		out.Bundles = append(out.Bundles, b)
	}
	return out, nil
}

// referrerDescriptors are the referrers of subject, at most
// MaxReferrerEntries, and whether there were more.
func (r *Registry) referrerDescriptors(ctx context.Context, subject name.Digest) ([]v1.Descriptor, bool, error) {
	repo := subject.Context()
	base := r.Transport
	if base == nil {
		base = remote.DefaultTransport
	}
	auth := authn.Anonymous
	if r.Keychain != nil {
		a, err := authn.Resolve(ctx, boundedKeychain{r.Keychain}, repo)
		if err != nil {
			return nil, false, fmt.Errorf("credentials for %s: %w", repo, err)
		}
		auth = a
	}
	rt, err := transport.NewWithContext(ctx, repo.Registry, auth, base, []string{repo.Scope(transport.PullScope)})
	if err != nil {
		return nil, false, err
	}
	client := &http.Client{Transport: rt, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("the registry redirected the referrers list to another host (%s)", req.URL.Host)
		}
		return nil
	}}
	u := &url.URL{Scheme: repo.Scheme(), Host: repo.RegistryStr(),
		Path:     fmt.Sprintf("/v2/%s/referrers/%s", repo.RepositoryStr(), subject.DigestStr()),
		RawQuery: url.Values{"artifactType": {BundleMediaType}}.Encode()}
	var out []v1.Descriptor
	seen := map[string]bool{}
	for page := 0; ; page++ {
		if page == maxReferrerPages {
			return nil, false, fmt.Errorf("%w: more than %d pages", ErrReferrersIncomplete, maxReferrerPages)
		}
		if seen[u.String()] {
			return nil, false, fmt.Errorf("%w: page %s is linked twice", ErrReferrersIncomplete, u.Redacted())
		}
		seen[u.String()] = true
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Accept", string(types.OCIImageIndex))
		resp, err := client.Do(req)
		if err != nil {
			return nil, false, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexBytes+1))
		_ = resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if page == 0 && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest ||
			resp.StatusCode == http.StatusNotAcceptable || resp.Header.Get("Content-Type") != string(types.OCIImageIndex)) {
			return r.tagSchemaReferrers(ctx, subject)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("referrers page %d: status %d", page+1, resp.StatusCode)
		}
		if len(body) > maxIndexBytes {
			return nil, false, fmt.Errorf("referrers page %d is over %d bytes", page+1, maxIndexBytes)
		}
		var idx v1.IndexManifest
		if err := json.NewDecoder(bytes.NewReader(body)).Decode(&idx); err != nil {
			return nil, false, fmt.Errorf("referrers page %d: %w", page+1, err)
		}
		out = append(out, idx.Manifests...)
		next, err := nextPage(resp.Header, u)
		if err != nil {
			return nil, false, err
		}
		if len(out) > maxReferrerEntries || (len(out) == maxReferrerEntries && next != nil) {
			return out[:maxReferrerEntries], true, nil
		}
		if next == nil {
			return out, false, nil
		}
		u = next
	}
}

// tagSchemaReferrers are the referrers of a registry without the Referrers
// API, from the referrers tag schema: one index.
func (r *Registry) tagSchemaReferrers(ctx context.Context, subject name.Digest) ([]v1.Descriptor, bool, error) {
	idx, err := remote.Referrers(subject, r.options(ctx)...)
	if err != nil {
		return nil, false, err
	}
	m, err := idx.IndexManifest()
	if err != nil {
		return nil, false, err
	}
	if len(m.Manifests) > maxReferrerEntries {
		return m.Manifests[:maxReferrerEntries], true, nil
	}
	return m.Manifests, false, nil
}

// linkNext matches one link of a Link header with rel="next".
var linkNext = regexp.MustCompile(`<([^>]+)>\s*;[^,]*\brel="?next"?`)

// nextPage is the Link header's next page, resolved against cur, or nil
// for the last page. A next page on another host or scheme is an error: the
// registry's credentials go to the registry only, and the list cannot be
// read whole without it.
func nextPage(h http.Header, cur *url.URL) (*url.URL, error) {
	for _, l := range h.Values("Link") {
		m := linkNext.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		next, err := cur.Parse(m[1])
		if err != nil {
			return nil, fmt.Errorf("%w: the registry returned a next link that is not a URL", ErrReferrersIncomplete)
		}
		if next.Host != cur.Host || next.Scheme != cur.Scheme {
			return nil, fmt.Errorf("%w: the registry returned an off-host next link (%s://%s)", ErrReferrersIncomplete, next.Scheme, next.Host)
		}
		return next, nil
	}
	return nil, nil
}
