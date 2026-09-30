// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestParseRecheckInterval: an empty, zero, negative or invalid value means
// the default, and a value below the minimum is raised to it, so a gate
// cannot re-evaluate in a hot loop.
func TestParseRecheckInterval(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{in: "", want: defaultRecheckInterval},
		{in: "0", want: defaultRecheckInterval},
		{in: "-1m", want: defaultRecheckInterval},
		{in: "soon", want: defaultRecheckInterval},
		{in: "1ms", want: minRecheckInterval},
		{in: "9s", want: minRecheckInterval},
		{in: "10s", want: 10 * time.Second},
		{in: "1m", want: time.Minute},
		{in: "5m", want: 5 * time.Minute},
		{in: "1h", want: time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, parseRecheckInterval(tt.in))
		})
	}
	assert.Equal(t, 10*time.Second, minRecheckInterval, "the documented minimum")
}
