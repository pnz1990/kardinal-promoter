// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package kubeevent

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type nopRecorder struct{}

func (nopRecorder) Eventf(runtime.Object, runtime.Object, string, string, string, string, ...interface{}) {
}

// TestLimited_Bounded: whatever the number of namespaces and Events, the
// Warning buckets and the dedupe memory stay at their bounds, least
// recently used out first.
//
// Covers PERF-EVENTS-01.
func TestLimited_Bounded(t *testing.T) {
	l := Limited(nopRecorder{}, "test-bounded", Limit{QPS: DefaultQPS, Burst: DefaultBurst}, Limit{QPS: DefaultWarningQPS, Burst: DefaultWarningBurst}).(*limited)
	for i := 0; i < 5*WarningNamespaces; i++ {
		ns := fmt.Sprintf("ns-%d", i)
		l.Eventf(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "p"}}, nil, corev1.EventTypeWarning, "R", "A", "%d", i)
	}
	assert.Equal(t, WarningNamespaces, l.warnings.len())
	assert.LessOrEqual(t, l.seen.len(), dedupeEntries)
	_, newest := l.warnings.get(fmt.Sprintf("ns-%d", 5*WarningNamespaces-1))
	_, oldest := l.warnings.get("ns-0")
	assert.True(t, newest)
	assert.False(t, oldest, "the least recently used namespace is out")

	c := newLRU[int](2)
	c.put("a", 1)
	c.put("b", 2)
	c.get("a")
	c.put("c", 3)
	_, okB := c.get("b")
	assert.False(t, okB, "b was the least recently used")
	assert.Equal(t, 2, c.len())
}
