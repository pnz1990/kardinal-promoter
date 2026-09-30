// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package kubeevent_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		note string
		want func(t *testing.T, got string)
	}{
		{
			name: "short note is unchanged",
			note: "PR #12 opened",
			want: func(t *testing.T, got string) { assert.Equal(t, "PR #12 opened", got) },
		},
		{
			name: "note at the limit is unchanged",
			note: strings.Repeat("a", kubeevent.NoteLimit),
			want: func(t *testing.T, got string) { assert.Len(t, got, kubeevent.NoteLimit) },
		},
		{
			name: "long ASCII note is cut to the limit",
			note: strings.Repeat("a", kubeevent.NoteLimit+500),
			want: func(t *testing.T, got string) {
				assert.Len(t, got, kubeevent.NoteLimit)
				assert.True(t, strings.HasSuffix(got, "..."))
			},
		},
		{
			name: "long multi-byte note is cut at a rune boundary",
			note: strings.Repeat("é", kubeevent.NoteLimit),
			want: func(t *testing.T, got string) {
				assert.LessOrEqual(t, len(got), kubeevent.NoteLimit)
				assert.True(t, utf8.ValidString(got))
				assert.True(t, strings.HasSuffix(got, "..."))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.want(t, kubeevent.Truncate(tt.note))
		})
	}
}

func TestEmit(t *testing.T) {
	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "default"}}

	t.Run("nil recorder is a no-op", func(t *testing.T) {
		assert.NotPanics(t, func() {
			kubeevent.Emit(nil, obj, corev1.EventTypeNormal, "Verified", "Verify", "ok")
		})
	})

	t.Run("percent signs in the note are kept", func(t *testing.T) {
		rec := events.NewFakeRecorder(1)
		kubeevent.Emit(rec, obj, corev1.EventTypeWarning, "Blocked", "Evaluate", "error-rate > 5% (https://x/?q=%2F)")
		require.Len(t, rec.Events, 1)
		assert.Equal(t, "Warning Blocked error-rate > 5% (https://x/?q=%2F)", <-rec.Events)
	})

	t.Run("long note is truncated", func(t *testing.T) {
		rec := events.NewFakeRecorder(1)
		kubeevent.Emit(rec, obj, corev1.EventTypeWarning, "Failed", "Promote", strings.Repeat("x", 5000))
		require.Len(t, rec.Events, 1)
		got := strings.TrimPrefix(<-rec.Events, "Warning Failed ")
		assert.Len(t, got, kubeevent.NoteLimit)
	})
}
