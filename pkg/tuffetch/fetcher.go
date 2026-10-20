// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package tuffetch is the TUF metadata fetcher kardinal uses to fetch a
// Sigstore trusted root: the controller's public-good root for image
// verification, and kardinal verify-receipt's. A 404 (or any other 4xx)
// ends the download at once, as a TUF root update needs: it asks for the
// next N.root.json until there is none. Only a 5xx, a 429 or a transport
// error is tried again, a few times, and every download, its retries
// included, is bounded by a deadline (#2218).
package tuffetch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// Defaults of a Fetcher's zero fields.
const (
	DefaultDeadline = time.Minute
	DefaultTries    = 3
	DefaultBackoff  = time.Second
	DefaultAgent    = "kardinal-promoter"
)

// Fetcher implements go-tuf's fetcher.Fetcher. go-tuf's own DefaultFetcher
// built with NewFetcherWithHTTPClient has no retry options, so it retries
// every error, a 404 included, for up to 15 minutes; this one does not.
type Fetcher struct {
	// Client makes each request; nil is http.DefaultClient. Its Timeout
	// bounds one attempt.
	Client *http.Client
	// Context is the parent of every download (a CLI's --timeout, the
	// controller's bound on a whole root fetch); nil is Background.
	Context context.Context
	// Deadline bounds one download, its retries included.
	Deadline time.Duration
	// Tries is how many times a download is attempted when the server
	// answers 5xx or 429 or the transport fails.
	Tries int
	// Backoff is the wait before the second attempt; it doubles after.
	Backoff time.Duration
	// UserAgent is sent with each request.
	UserAgent string
}

// DownloadFile implements fetcher.Fetcher: it returns the body of urlPath,
// at most maxLength bytes. A status other than 200 is a
// *metadata.ErrDownloadHTTP, as go-tuf's updater expects (it stops a root
// update on 404 or 403). The timeout argument is deprecated in go-tuf and
// not used.
func (f *Fetcher) DownloadFile(urlPath string, maxLength int64, _ time.Duration) ([]byte, error) {
	parent := f.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, cmp.Or(f.Deadline, DefaultDeadline))
	defer cancel()
	tries := cmp.Or(f.Tries, DefaultTries)
	wait := cmp.Or(f.Backoff, DefaultBackoff)
	var last error
	for try := 1; ; try++ {
		data, retry, err := f.get(ctx, urlPath, maxLength)
		if err == nil || !retry || try >= tries {
			return data, err
		}
		last = err
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, fmt.Errorf("download %s: %w (last attempt: %w)", urlPath, ctx.Err(), last)
		case <-t.C:
		}
		wait *= 2
	}
}

// get makes one attempt. retry says whether another may succeed: a 5xx,
// a 429 or a transport error, not a 4xx or a body over maxLength.
func (f *Fetcher) get(ctx context.Context, urlPath string, maxLength int64) (data []byte, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlPath, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", cmp.Or(f.UserAgent, DefaultAgent))
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		// A deadline or cancellation of the download itself is final.
		return nil, ctx.Err() == nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		retry := res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests
		return nil, retry, &metadata.ErrDownloadHTTP{StatusCode: res.StatusCode, URL: urlPath}
	}
	if h := res.Header.Get("Content-Length"); h != "" {
		if n, err := strconv.ParseInt(h, 10, 64); err == nil && n > maxLength {
			return nil, false, &metadata.ErrDownloadLengthMismatch{
				Msg: fmt.Sprintf("download failed for %s, length %d is larger than expected %d", urlPath, n, maxLength)}
		}
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, maxLength+1))
	if err != nil {
		return nil, ctx.Err() == nil && !errors.Is(err, context.Canceled), err
	}
	if int64(len(data)) > maxLength {
		return nil, false, &metadata.ErrDownloadLengthMismatch{
			Msg: fmt.Sprintf("download failed for %s, length %d is larger than expected %d", urlPath, len(data), maxLength)}
	}
	return data, false, nil
}
