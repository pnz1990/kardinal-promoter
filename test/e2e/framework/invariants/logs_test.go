// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package invariants

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestCollector() *Collector {
	return &Collector{seen: map[string]bool{}, unexpected: map[string]*LogGroup{}, reconcile: map[string]*LogGroup{}}
}

func TestCollectorScan(t *testing.T) {
	log := strings.Join([]string{
		`{"level":"info","message":"graph created"}`,
		`{"level":"warn","message":"slow"}`,
		`{"level":"error","logger":"leaderelection","msg":"Error initially creating lease lock","error":"leases \"x\" already exists"}`,
		`{"level":"error","msg":"Reconciler error","controller":"promotionstep","error":"patch step 1234: Unauthorized"}`,
		`{"level":"error","msg":"Reconciler error","controller":"promotionstep","error":"patch step 98765: Unauthorized"}`,
		`{"level":"error","msg":"Reconciler error","controller":"bundle","error":"the object has been modified; please apply your changes to the latest version and try again"}`,
		`{"level":"error","msg":"Observed a panic in reconciler: boom"}`,
		`==================`,
		`WARNING: DATA RACE`,
		`Write at 0x00c000 by goroutine 7:`,
		`  main.f()`,
		`==================`,
		`not json at all`,
		`panic: runtime error: index out of range`,
		`goroutine 1 [running]:`,
	}, "\n")
	c := newTestCollector()
	c.scan(strings.NewReader(log), "pod-a", false)
	s := c.Summary()

	assert.Equal(t, 9, s.Lines, "every line outside a race or panic block is counted")
	assert.Equal(t, 1, s.Warnings)
	assert.Equal(t, 5, s.Errors)
	assert.Equal(t, 2, s.Benign, "the lease race and the conflict")
	require.Len(t, s.Races, 1)
	assert.Equal(t, []string{"WARNING: DATA RACE", "Write at 0x00c000 by goroutine 7:", "  main.f()", "=================="}, s.Races[0].Lines)
	require.Len(t, s.Panics, 2, "the recovered reconciler panic and the runtime panic")
	assert.Equal(t, "panic: runtime error: index out of range", s.Panics[1].Lines[0])
	require.Len(t, s.Unexpected, 1, "two Unauthorized lines that differ only in a number are one group")
	assert.Equal(t, 2, s.Unexpected[0].Count)
	assert.Contains(t, s.Unexpected[0].Key, "patch step N: Unauthorized")
	require.Len(t, s.ReconcileErrors, 2)
	assert.Equal(t, "promotionstep: patch step N: Unauthorized", s.ReconcileErrors[0].Key)
}

func TestCollectorScanKro(t *testing.T) {
	c := newTestCollector()
	c.scan(strings.NewReader("{\"level\":\"error\",\"msg\":\"x\"}\npanic: boom\nstack"), "kro-1", true)
	s := c.Summary()
	assert.Zero(t, s.Errors, "kro's error lines are not the controller's")
	require.Len(t, s.KroPanics, 1)
	assert.Equal(t, []string{"panic: boom", "stack"}, s.KroPanics[0].Lines)
}

func TestCheckLogs(t *testing.T) {
	s := LogSummary{Pods: []string{"kardinal-promoter-x/controller#0"},
		Unexpected: []LogGroup{{Key: "a", Count: 3, Example: Line{Message: "git clone failed", Error: "connection refused"}}}}
	assert.Len(t, checkLogs(s, Options{}).Violations, 1)
	o := Options{}
	o.Allow = append(o.Allow, Benign[0])
	assert.Len(t, checkLogs(s, o).Violations, 1, "an unrelated pattern does not allow it")
	s.Races = []Block{{Pod: "p", Kind: "race", Lines: []string{"WARNING: DATA RACE"}}}
	assert.Len(t, checkLogs(s, Options{}).Violations, 2)
}

func TestCheckLogsNothingRead(t *testing.T) {
	v := checkLogs(LogSummary{Pods: []string{"kro-1/kro#0"}, StreamErrors: []string{"stream broke"}}, Options{}).Violations
	require.Len(t, v, 2)
	assert.Contains(t, v[0], "no controller container")
	assert.Contains(t, v[1], "stream broke")
}

func TestScanReportsScannerErrors(t *testing.T) {
	c := newTestCollector()
	long := strings.Repeat("x", 5*1024*1024)
	assert.Error(t, c.scan(strings.NewReader(long), "p", false), "a line past the scanner's buffer is an error")
}
