// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestPermanent proves the marker the PromotionStep reconciler uses to fail a
// step at once instead of retrying it: it survives the engine's wrapping,
// leaves the message alone and keeps the cause reachable.
func TestPermanent(t *testing.T) {
	cause := fmt.Errorf("read values: %w", fs.ErrNotExist)
	tests := []struct {
		name          string
		err           error
		wantPermanent bool
		wantMsg       string
	}{
		{name: "marked", err: steps.Permanent(cause), wantPermanent: true, wantMsg: cause.Error()},
		{name: "marked, then wrapped by the engine", err: fmt.Errorf("step helm-set-image: %w", steps.Permanent(cause)),
			wantPermanent: true, wantMsg: "step helm-set-image: " + cause.Error()},
		{name: "unmarked", err: fmt.Errorf("step git-push: %w", context.DeadlineExceeded),
			wantMsg: "step git-push: " + context.DeadlineExceeded.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantPermanent, errors.Is(tt.err, steps.ErrPermanent))
			assert.Equal(t, tt.wantMsg, tt.err.Error())
		})
	}
	assert.ErrorIs(t, steps.Permanent(cause), fs.ErrNotExist, "the cause stays in the chain")
	assert.NoError(t, steps.Permanent(nil))
}
