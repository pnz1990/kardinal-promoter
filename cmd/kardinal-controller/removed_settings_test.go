// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"flag"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRemovedSettings verifies that a deployment still setting --shard or
// --pipeline-admission-webhook, as a flag or through its environment
// variable, stops the controller with a message that says what to remove
// (#1321, #1264), and that leaving them unset starts it.
func TestRemovedSettings(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr []string
	}{
		{name: "nothing set"},
		{name: "admission webhook explicitly off", args: []string{"--pipeline-admission-webhook=false"}},
		{name: "admission webhook env not true", env: map[string]string{"KARDINAL_PIPELINE_ADMISSION_WEBHOOK": "false"}},
		{name: "shard flag", args: []string{"--shard=eu"},
			wantErr: []string{"--shard", "is removed", "reconciles every environment", "kardinal-agent"}},
		{name: "shard env", env: map[string]string{"KARDINAL_SHARD": "eu"},
			wantErr: []string{"KARDINAL_SHARD", "is removed"}},
		{name: "admission webhook flag", args: []string{"--pipeline-admission-webhook"},
			wantErr: []string{"--pipeline-admission-webhook", "is removed", "delete your ValidatingWebhookConfiguration"}},
		{name: "admission webhook env", env: map[string]string{"KARDINAL_PIPELINE_ADMISSION_WEBHOOK": "true"},
			wantErr: []string{"KARDINAL_PIPELINE_ADMISSION_WEBHOOK", "delete your ValidatingWebhookConfiguration"}},
		{name: "both", args: []string{"--shard=eu", "--pipeline-admission-webhook"},
			wantErr: []string{"--shard", "--pipeline-admission-webhook"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			r := bindRemovedFlags(fs, func(k string) string { return tc.env[k] })
			require.NoError(t, fs.Parse(tc.args))
			err := r.err()
			if len(tc.wantErr) == 0 {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, w := range tc.wantErr {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}
