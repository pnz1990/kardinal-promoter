// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// freshGets is a client whose Get reads the API server (the manager's
// APIReader) instead of the informer cache; List, writes and the rest are
// the wrapped client's. The UI's lifecycle actions read an object, decide
// and write it (pause, resume, release a hold, reject, override): through
// the cache, a resume right after a pause read the Pipeline still unpaused,
// found nothing to change and left it paused. A fresh Get reads the write
// that came before.
type freshGets struct {
	client.Client
	reader client.Reader
}

// Get reads key from the API server.
func (c freshGets) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.reader.Get(ctx, key, obj, opts...)
}

// fresh is c with Gets from the API server, or c when the server has no
// API reader (tests).
func (s *uiAPIServer) fresh(c client.Client) client.Client {
	if s.apiReader == nil {
		return c
	}
	return freshGets{Client: c, reader: s.apiReader}
}
