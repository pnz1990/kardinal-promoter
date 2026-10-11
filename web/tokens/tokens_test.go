// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package tokens

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestColor (C4): a status token reads as its light and dark hex, through
// references ({color.thread.text}) on either side, and a missing or
// non-colour token is not ok.
//
// Covers CLI-STYLE-01.
func TestColor(t *testing.T) {
	for _, path := range []string{"color.status.verified.fg", "color.status.promoting.fg", "color.status.waiting.fg",
		"color.status.blocked.fg", "color.status.failed.fg", "color.status.failed.fill", "color.status.failed.on-fill",
		"color.status.superseded.fg", "color.ink"} {
		c, ok := Color(path)
		require.True(t, ok, path)
		assert.Regexp(t, `^#[0-9A-Fa-f]{6}$`, c.Light, path)
		assert.Regexp(t, `^#[0-9A-Fa-f]{6}$`, c.Dark, path)
	}
	promoting, _ := Color("color.status.promoting.fg")
	thread, _ := Color("color.thread.text")
	assert.Equal(t, thread, promoting, "a reference resolves to its token")
	onFill, _ := Color("color.status.failed.on-fill")
	inverse, _ := Color("color.ink.inverse")
	assert.Equal(t, inverse.Light, onFill.Light, "a side's reference takes that side")
	for _, bad := range []string{"color.status.nope.fg", "stroke.thread", "color", ""} {
		_, ok := Color(bad)
		assert.False(t, ok, bad)
	}
}
