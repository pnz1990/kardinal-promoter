// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRead(t *testing.T) {
	tests := []struct {
		name              string
		in                string
		ok                bool
		pass, fail, skip  int
		pkgFailed         bool
		wantOut, wantInMD string
	}{
		{
			name: "all pass",
			in: `{"Action":"run","Test":"TestCore_A"}
{"Action":"output","Test":"TestCore_A","Output":"=== RUN   TestCore_A\n"}
{"Action":"pass","Test":"TestCore_A","Elapsed":71.2}
{"Action":"pass","Test":"TestCore_B","Elapsed":3}
{"Action":"pass","Elapsed":74.3}`,
			ok: true, pass: 2,
			wantOut: "=== RUN   TestCore_A\n", wantInMD: "| `TestCore_A` | pass | 71s |",
		},
		{
			name: "a skip fails the run",
			in: `{"Action":"pass","Test":"TestCore_A"}
{"Action":"skip","Test":"TestCore_B"}
{"Action":"pass"}`,
			pass: 1, skip: 1, wantInMD: "FAILED",
		},
		{
			name: "a failed test fails the run",
			in: `{"Action":"fail","Test":"TestCore_A"}
{"Action":"fail"}`,
			fail: 1, pkgFailed: true,
		},
		{
			name: "no tests ran",
			in:   `{"Action":"output","Output":"testing: warning: no tests to run\n"}` + "\n" + `{"Action":"pass"}`,
		},
		{
			name: "build error before any event",
			in: `# github.com/x/live
live/core_test.go:1: undefined: foo
{"Action":"fail"}`,
			pkgFailed: true, wantOut: "live/core_test.go:1: undefined: foo\n",
		},
		{
			name: "-count=2 keeps both results",
			in: `{"Action":"pass","Test":"TestCore_A"}
{"Action":"fail","Test":"TestCore_A"}`,
			pass: 1, fail: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			s, err := read(strings.NewReader(tt.in), &out)
			require.NoError(t, err)
			assert.Equal(t, tt.ok, s.ok())
			assert.Equal(t, tt.pass, s.count("pass"))
			assert.Equal(t, tt.fail, s.count("fail"))
			assert.Equal(t, tt.skip, s.count("skip"))
			assert.Equal(t, tt.pkgFailed, s.pkgFailed)
			if tt.wantOut != "" {
				assert.Contains(t, out.String(), tt.wantOut)
			}
			if tt.wantInMD != "" {
				assert.Contains(t, s.markdown("core"), tt.wantInMD)
			}
		})
	}
}
