// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"errors"
	"flag"
	"strings"
)

// removedSettings holds controller settings that were removed. They are still
// parsed so a deployment that sets one stops at startup with a message that
// says what to do, instead of "flag provided but not defined" or, for an
// environment variable, silently doing something else than before.
type removedSettings struct {
	// shard was distributed mode: the controller skipped PromotionSteps
	// labelled for another shard, and kardinal-agent reconciled them.
	shard string
	// pipelineAdmissionWebhook mounted POST /webhook/validate/pipeline.
	pipelineAdmissionWebhook bool
}

// bindRemovedFlags registers the removed flags on fs, defaulting each to its
// environment variable as the flags did before they were removed.
func bindRemovedFlags(fs *flag.FlagSet, getenv func(string) string) *removedSettings {
	r := &removedSettings{}
	fs.StringVar(&r.shard, "shard", getenv("KARDINAL_SHARD"),
		"Removed: distributed mode is gone. Setting it (or KARDINAL_SHARD) stops the controller.")
	fs.BoolVar(&r.pipelineAdmissionWebhook, "pipeline-admission-webhook",
		getenv("KARDINAL_PIPELINE_ADMISSION_WEBHOOK") == "true",
		"Removed: the Pipeline admission webhook is gone. Setting it (or "+
			"KARDINAL_PIPELINE_ADMISSION_WEBHOOK=true) stops the controller.")
	return r
}

// err returns why the controller must not start, or nil when no removed
// setting is set.
func (r *removedSettings) err() error {
	var msgs []string
	if r.shard != "" {
		msgs = append(msgs, "--shard (KARDINAL_SHARD, chart value controller.shard) is removed: "+
			"distributed mode is gone and this controller reconciles every environment; remove the "+
			"setting, remove shard from your Pipeline environments and delete any kardinal-agent Deployment")
	}
	if r.pipelineAdmissionWebhook {
		msgs = append(msgs, "--pipeline-admission-webhook (KARDINAL_PIPELINE_ADMISSION_WEBHOOK) is removed: "+
			"the Pipeline reconciler reports the same checks as Ready=False and \"kardinal validate\" runs "+
			"them offline; remove the setting and delete your ValidatingWebhookConfiguration for "+
			"/webhook/validate/pipeline")
	}
	if len(msgs) == 0 {
		return nil
	}
	return errors.New(strings.Join(msgs, "; "))
}
