// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"time"

	"github.com/rs/zerolog"
)

// tracingFlushTimeout bounds the export of buffered spans at shutdown; it
// fits in the controller's 30s shutdown budget.
const tracingFlushTimeout = 5 * time.Second

// tracingFlusher is a manager runnable that waits for the manager to stop
// and then flushes the trace exporter. It runs on every replica, leader or
// not.
type tracingFlusher struct {
	shutdown func(context.Context) error
	log      zerolog.Logger
}

// NeedLeaderElection is false: standby replicas export spans too.
func (tracingFlusher) NeedLeaderElection() bool { return false }

// Start blocks until ctx is done, then flushes.
func (f tracingFlusher) Start(ctx context.Context) error {
	<-ctx.Done()
	flushCtx, cancel := context.WithTimeout(context.Background(), tracingFlushTimeout)
	defer cancel()
	if err := f.shutdown(flushCtx); err != nil {
		f.log.Warn().Err(err).Msg("flush traces at shutdown")
	}
	return nil
}
