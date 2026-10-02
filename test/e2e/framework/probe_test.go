// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Only a connection that got no answer counts as dropped by a policy; a
// refused one (nothing listens, or no endpoint) does not.
func TestCurlResult_TimedOut(t *testing.T) {
	for _, tt := range []struct {
		name string
		res  CurlResult
		want bool
	}{
		{"connect timeout", CurlResult{Err: "curl: (28) Connection timed out after 3002 milliseconds\ncommand terminated with exit code 28"}, true},
		{"max time", CurlResult{Err: "curl: (28) Operation timed out after 13001 milliseconds with 0 bytes received"}, true},
		{"refused", CurlResult{Err: "curl: (7) Failed to connect to 10.96.0.1 port 8080 after 1 ms: Couldn't connect to server"}, false},
		{"answered", CurlResult{Code: 200}, false},
		{"exec failed", CurlResult{Err: "error: unable to upgrade connection: container not found (\"probe\")"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.res.TimedOut())
		})
	}
}
