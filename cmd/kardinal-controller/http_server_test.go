// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover C07-controller-22: the webhook and UI servers had no
// timeouts, were not stopped on shutdown, only logged a bind failure (the pod
// stayed Ready without its listener), and fell back to plain HTTP when only
// one TLS flag was set.

func TestHTTPServer_Config(t *testing.T) {
	tests := []struct {
		name     string
		cert     string
		key      string
		wantErr  bool
		wantTLS  bool
		wantAddr string
	}{
		{name: "plain HTTP", wantAddr: ":8083"},
		{name: "TLS", cert: "/tls/tls.crt", key: "/tls/tls.key", wantTLS: true, wantAddr: ":8083"},
		{name: "cert without key", cert: "/tls/tls.crt", wantErr: true},
		{name: "key without cert", key: "/tls/tls.key", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := newHTTPServer("webhook", ":8083", http.NotFoundHandler(), tt.cert, tt.key, zerolog.Nop())
			if tt.wantErr {
				require.ErrorIs(t, err, errPartialTLS)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantAddr, s.srv.Addr)
			assert.Equal(t, tt.wantTLS, s.certFile != "")
			assert.Equal(t, 10*time.Second, s.srv.ReadHeaderTimeout)
			assert.Equal(t, 30*time.Second, s.srv.ReadTimeout)
			assert.Equal(t, 60*time.Second, s.srv.WriteTimeout)
			assert.Equal(t, 120*time.Second, s.srv.IdleTimeout)
			assert.False(t, s.NeedLeaderElection(), "every replica serves")
		})
	}
}

func TestHTTPServer_BindFailureIsReturned(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer taken.Close()

	s, err := newHTTPServer("ui", taken.Addr().String(), http.NotFoundHandler(), "", "", zerolog.Nop())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = s.Start(ctx)
	require.Error(t, err, "a bind failure must stop the manager, not only be logged")
	assert.Contains(t, err.Error(), "ui server: listen on "+taken.Addr().String())
}

func TestHTTPServer_GracefulShutdown(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "done")
	})
	s, err := newHTTPServer("webhook", "127.0.0.1:0", handler, "", "", zerolog.Nop())
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	url := "http://" + ln.Addr().String() + "/"

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.serve(ctx, ln) }()

	type result struct {
		body string
		err  error
	}
	inflight := make(chan result, 1)
	go func() {
		resp, err := http.Get(url)
		if err != nil {
			inflight <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		inflight <- result{body: string(b), err: err}
	}()
	<-started

	cancel()
	select {
	case err := <-served:
		t.Fatalf("server stopped before the in-flight request finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	r := <-inflight
	require.NoError(t, r.err)
	assert.Equal(t, "done", r.body, "the in-flight request is drained, not cut off")
	select {
	case err := <-served:
		assert.NoError(t, err, "a clean shutdown is not an error")
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after shutdown")
	}
	_, err = http.Get(url)
	assert.Error(t, err, "no new connections after shutdown")
}

func TestHTTPServer_ServesTLS(t *testing.T) {
	certFile, keyFile := writeSelfSignedCert(t)
	s, err := newHTTPServer("ui", "127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}), certFile, keyFile, zerolog.Nop())
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.serve(ctx, ln) }()
	defer func() {
		cancel()
		assert.NoError(t, <-served)
	}()

	httpsClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // self-signed test cert
	}}
	resp, err := httpsClient.Get("https://" + ln.Addr().String() + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "secure", string(body))
	require.NotNil(t, resp.TLS)
}

func TestHTTPServer_BadCertIsReturned(t *testing.T) {
	dir := t.TempDir()
	s, err := newHTTPServer("ui", "127.0.0.1:0", http.NotFoundHandler(),
		filepath.Join(dir, "missing.crt"), filepath.Join(dir, "missing.key"), zerolog.Nop())
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	assert.Error(t, s.serve(ctx, ln), "an unreadable certificate must stop the manager")
}

func writeSelfSignedCert(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certFile, keyFile
}
