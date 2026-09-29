// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

// Timeouts for the webhook and UI servers. ReadHeaderTimeout and ReadTimeout
// bound slow clients (slowloris); WriteTimeout covers the slowest handler
// (bundle creation and the UI list endpoints make a few API calls each).
const (
	serverReadHeaderTimeout = 10 * time.Second
	serverReadTimeout       = 30 * time.Second
	serverWriteTimeout      = 60 * time.Second
	serverIdleTimeout       = 120 * time.Second
	// serverShutdownTimeout is how long in-flight requests get to finish on
	// shutdown. It stays under gracefulShutdownTimeout so the manager does not
	// give up on the server first.
	serverShutdownTimeout = 20 * time.Second
)

// errPartialTLS is returned when only one of the TLS flags is set. Serving
// plain HTTP in that case would silently drop the TLS the operator asked for.
var errPartialTLS = errors.New("--tls-cert-file and --tls-key-file must be set together")

// httpServer runs one of the controller's HTTP listeners as a manager
// Runnable. The manager starts it after the informer caches sync, a listen or
// serve error stops the manager (so the pod does not stay Ready without its
// listener), and on shutdown in-flight requests are drained.
type httpServer struct {
	name            string
	srv             *http.Server
	certFile        string
	keyFile         string
	shutdownTimeout time.Duration
	log             zerolog.Logger
}

// newHTTPServer builds a server for addr. TLS is used when both certFile and
// keyFile are set, plain HTTP when neither is; setting only one is an error.
func newHTTPServer(name, addr string, handler http.Handler, certFile, keyFile string, log zerolog.Logger) (*httpServer, error) {
	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("%s server: %w", name, errPartialTLS)
	}
	return &httpServer{
		name: name,
		srv: &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: serverReadHeaderTimeout,
			ReadTimeout:       serverReadTimeout,
			WriteTimeout:      serverWriteTimeout,
			IdleTimeout:       serverIdleTimeout,
		},
		certFile:        certFile,
		keyFile:         keyFile,
		shutdownTimeout: serverShutdownTimeout,
		log:             log.With().Str("server", name).Logger(),
	}, nil
}

// NeedLeaderElection is false: every replica serves, not only the leader.
func (s *httpServer) NeedLeaderElection() bool { return false }

// Start listens on the server address and serves until ctx is done.
func (s *httpServer) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("%s server: listen on %s: %w", s.name, s.srv.Addr, err)
	}
	return s.serve(ctx, ln)
}

// serve runs the server on ln and shuts it down gracefully when ctx is done.
func (s *httpServer) serve(ctx context.Context, ln net.Listener) error {
	tlsEnabled := s.certFile != ""
	errCh := make(chan error, 1)
	go func() {
		if tlsEnabled {
			errCh <- s.srv.ServeTLS(ln, s.certFile, s.keyFile)
			return
		}
		errCh <- s.srv.Serve(ln)
	}()
	s.log.Info().Str("addr", ln.Addr().String()).Bool("tls", tlsEnabled).Msg("server listening")

	select {
	case err := <-errCh:
		return fmt.Errorf("%s server: %w", s.name, err)
	case <-ctx.Done():
	}

	s.log.Info().Msg("shutting down server")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()
	if err := s.srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("%s server: shutdown: %w", s.name, err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s server: %w", s.name, err)
	}
	return nil
}
