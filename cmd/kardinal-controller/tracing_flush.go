// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"time"

	"github.com/rs/zerolog"
)

// tracingFlushTimeout bounds the export of buffered spans at shutdown.
const tracingFlushTimeout = 5 * time.Second

// runThenFlush runs run (the manager, until it has stopped every runnable)
// and then flushes the trace exporter with shutdown, so spans ended while
// reconcilers and servers drained are exported. It returns run's error.
func runThenFlush(run func() error, shutdown func(context.Context) error, log zerolog.Logger) error {
	err := run()
	ctx, cancel := context.WithTimeout(context.Background(), tracingFlushTimeout)
	defer cancel()
	if ferr := shutdown(ctx); ferr != nil {
		log.Warn().Err(ferr).Msg("flush traces at shutdown")
	}
	return err
}
