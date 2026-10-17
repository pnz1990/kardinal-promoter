// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"

	pbs "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	rekortilespb "github.com/sigstore/rekor-tiles/v2/pkg/generated/protobuf"
	"google.golang.org/protobuf/encoding/protojson"
)

// maxRekorV2Response bounds a Rekor v2 response body.
const maxRekorV2Response = 1 << 20

// rekorV2Writer is a Rekor v2 (rekor-tiles) write client on the signer's
// transport: rekor-tiles' own writer always uses http.DefaultTransport, which
// would skip the egress guard and allowlist. sigstore-go logs a DSSE
// envelope in Rekor v2 as a hashedrekord entry, the only kind v2 accepts.
type rekorV2Writer struct {
	base   *url.URL
	client *http.Client
}

func newRekorV2Writer(rawURL string, transport http.RoundTripper, timeout time.Duration) (*rekorV2Writer, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid Rekor URL %q", rawURL)
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &rekorV2Writer{base: u, client: &http.Client{Transport: transport, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Add logs entry and returns its transparency log entry.
func (w *rekorV2Writer) Add(ctx context.Context, entry any) (*pbs.TransparencyLogEntry, error) {
	h, ok := entry.(*rekortilespb.HashedRekordRequestV002)
	if !ok {
		return nil, fmt.Errorf("unsupported Rekor v2 entry type %T", entry)
	}
	body, err := protojson.Marshal(&rekortilespb.CreateEntryRequest{
		Spec: &rekortilespb.CreateEntryRequest_HashedRekordRequestV002{HashedRekordRequestV002: h}})
	if err != nil {
		return nil, fmt.Errorf("marshal Rekor v2 entry: %w", err)
	}
	endpoint := *w.base
	endpoint.Path = path.Join(endpoint.Path, "/api/v2/log/entries")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build the Rekor v2 request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("log in Rekor v2: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxRekorV2Response))
	if err != nil {
		return nil, fmt.Errorf("read the Rekor v2 response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("the Rekor v2 log answered %d: %s", resp.StatusCode, bytes.TrimSpace(out))
	}
	var tle pbs.TransparencyLogEntry
	if err := protojson.Unmarshal(out, &tle); err != nil {
		return nil, fmt.Errorf("parse the Rekor v2 entry: %w", err)
	}
	return &tle, nil
}
