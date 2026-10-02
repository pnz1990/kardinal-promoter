// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogStreamReconnects(t *testing.T) {
	t1 := time.Date(2026, 10, 1, 6, 0, 52, 100, time.UTC)
	t2 := t1.Add(time.Millisecond)
	t3 := t2.Add(time.Second)
	line := func(at time.Time, text string) string { return at.Format(time.RFC3339Nano) + " " + text + "\n" }

	// The first stream ends early; the reconnected one starts at SinceTime's
	// second, so it repeats lines the first one read.
	first := line(t1, "a") + line(t2, "b") + line(t2, "c")
	second := line(t1, "a") + line(t2, "b") + line(t2, "c") + line(t2, "d") + line(t3, "e")
	var since []time.Time
	open := func(from time.Time) (io.ReadCloser, error) {
		since = append(since, from)
		return io.NopCloser(strings.NewReader(second)), nil
	}
	checks := 0
	running := func() bool { checks++; return checks == 1 }

	s := &LogStream{done: make(chan struct{})}
	go s.follow(context.Background(), io.NopCloser(strings.NewReader(first)), open, running)
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not end after the container stopped")
	}

	require.Equal(t, []time.Time{t2}, since, "it reconnects once, from the last line read")
	var texts []string
	for _, l := range s.Lines() {
		texts = append(texts, l.Text)
	}
	assert.Equal(t, []string{"a", "b", "c", "d", "e"}, texts, "each line once, without its timestamp")
	got, ok := s.Find("e")
	require.True(t, ok)
	assert.True(t, got.At.Equal(t3), "At is the log timestamp")
}

func TestLogStreamEndsWithTheContainer(t *testing.T) {
	open := func(time.Time) (io.ReadCloser, error) {
		t.Error("reopened the log of a stopped container")
		return nil, io.EOF
	}
	s := &LogStream{done: make(chan struct{})}
	go s.follow(context.Background(), io.NopCloser(strings.NewReader("no timestamp\n")), open, func() bool { return false })
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not end")
	}
	lines := s.Lines()
	require.Len(t, lines, 1)
	assert.Equal(t, "no timestamp", lines[0].Text, "a line without a timestamp is kept whole")
}
