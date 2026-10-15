// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package prstatus

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

// TestClipMerger (#2003 QA): a merger name past the CRD's 253 bytes is cut,
// on a UTF-8 boundary, so the status patch never fails on it.
func TestClipMerger(t *testing.T) {
	assert.Equal(t, "user1", clipMerger("user1"))
	long := strings.Repeat("é", 200) // 400 bytes
	got := clipMerger(long)
	assert.LessOrEqual(t, len(got), maxMergerLength)
	assert.True(t, utf8.ValidString(got))
}
