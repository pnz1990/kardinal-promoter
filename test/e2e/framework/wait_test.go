// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// A check still running when the window ends keeps a live context, so its
// API calls don't fail with the window's deadline.
func TestConsistently_CheckOutlivesWindow(t *testing.T) {
	calls := 0
	Consistently(t, 50*time.Millisecond, "the hold", func(ctx context.Context) (bool, string) {
		calls++
		time.Sleep(100 * time.Millisecond)
		return ctx.Err() == nil, fmt.Sprint(ctx.Err())
	})
	assert.Equal(t, 1, calls)
}
