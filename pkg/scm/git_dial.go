// Copyright 2026 The kardinal-promoter Authors.
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

package scm

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"golang.org/x/net/proxy"
)

// gitIdleTimeout bounds how long a git connection, ssh or HTTPS, may go
// without sending or receiving a byte: a server that stalls mid-transfer
// fails the clone or push after it instead of holding the step's worker for
// ever. A variable so a test can shorten it.
var gitIdleTimeout = 5 * time.Minute

// dialScheme is the proxy scheme kardinal's git client sets on ssh
// endpoints (ProxyOptions) so that go-git dials through dialScope: go-git
// has no other hook for the connection it opens for a fetch.
const dialScheme = "kardinal-dial"

// dialScope ties the connections of one git operation to its context: they
// are closed when the context ends, and closeAll closes them at once (a
// handshake that does not finish in time).
type dialScope struct {
	id    string
	ctx   context.Context
	mu    sync.Mutex
	conns []net.Conn
}

var (
	dialScopes sync.Map // id -> *dialScope
	dialSeq    atomic.Uint64
)

func init() {
	proxy.RegisterDialerType(dialScheme, func(u *url.URL, _ proxy.Dialer) (proxy.Dialer, error) {
		return scopedDialer{id: u.Host}, nil
	})
	// HTTPS (and HTTP) git: the same idle bound on every connection.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return newIdleConn(c, gitIdleTimeout), nil
	}
	httpClient := gogithttp.NewClient(&http.Client{Transport: tr})
	client.InstallProtocol("https", httpClient)
	client.InstallProtocol("http", httpClient)
}

// newDialScope registers ctx for the ssh connections of one git operation
// and returns the ProxyOptions that make go-git dial through it, and the
// release that forgets it.
func newDialScope(ctx context.Context) (transport.ProxyOptions, *dialScope, func()) {
	id := strconv.FormatUint(dialSeq.Add(1), 10)
	s := &dialScope{id: id, ctx: ctx}
	dialScopes.Store(id, s)
	return transport.ProxyOptions{URL: dialScheme + "://" + id}, s, func() { dialScopes.Delete(id) }
}

// scopeOf returns the dialScope of an endpoint kardinal's git client made,
// or nil.
func scopeOf(ep *transport.Endpoint) *dialScope {
	u, err := url.Parse(ep.Proxy.URL)
	if err != nil || u.Scheme != dialScheme {
		return nil
	}
	s, ok := dialScopes.Load(u.Host)
	if !ok {
		return nil
	}
	return s.(*dialScope)
}

func (s *dialScope) track(c net.Conn) {
	s.mu.Lock()
	s.conns = append(s.conns, c)
	s.mu.Unlock()
}

// closeAll closes every connection of the scope.
func (s *dialScope) closeAll() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// scopedDialer is the proxy.Dialer of dialScheme: a direct TCP dial whose
// connection has the idle bound and belongs to the scope's context.
type scopedDialer struct{ id string }

func (d scopedDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d scopedDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var s *dialScope
	if v, ok := dialScopes.Load(d.id); ok {
		s = v.(*dialScope)
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(s.ctx, cancel)
		defer stop()
	}
	c, err := (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	ic := newIdleConn(c, gitIdleTimeout)
	if s != nil {
		stop := context.AfterFunc(s.ctx, func() { _ = ic.Close() })
		ic.mu.Lock()
		ic.stop = stop
		ic.mu.Unlock()
		s.track(ic)
	}
	return ic, nil
}

// idleConn fails a Read or Write that waits more than idle for the other
// side. An explicit deadline (SetDeadline, used for a handshake) takes the
// place of the idle bound until it is cleared.
type idleConn struct {
	net.Conn
	idle time.Duration
	stop func() bool

	mu    sync.Mutex
	fixed time.Time
}

func newIdleConn(c net.Conn, idle time.Duration) *idleConn {
	return &idleConn{Conn: c, idle: idle}
}

func (c *idleConn) deadline() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.fixed.IsZero() {
		return c.fixed
	}
	return time.Now().Add(c.idle)
}

func (c *idleConn) Read(p []byte) (int, error) {
	if err := c.SetReadDeadline(c.deadline()); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}

func (c *idleConn) Write(p []byte) (int, error) {
	if err := c.SetWriteDeadline(c.deadline()); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

func (c *idleConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.fixed = t
	c.mu.Unlock()
	return c.Conn.SetDeadline(t)
}

func (c *idleConn) Close() error {
	c.mu.Lock()
	stop := c.stop
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
	return c.Conn.Close()
}
