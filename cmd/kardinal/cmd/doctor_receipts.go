// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// receiptGrowthWarnBytes is the receipts' total size past which doctor
// warns when retention is off: about an eighth of etcd's default 8 GB
// quota, which a receipt (up to about 600 KiB in etcd) shares with every
// other object (#2136).
const receiptGrowthWarnBytes = 1 << 30

// checkReceiptGrowth reports the promotion receipts' count and size, from
// the Pipelines' status.environments[].receipts it can read, and warns
// when they pass receiptGrowthWarnBytes while the controller keeps every
// receipt (no --receipt-retention-max-age). Without receipts it passes.
// shown is false when there is nothing to report: no receipt, and the
// controller does not sign them (or its Deployment cannot be read), so a
// cluster without receipts sees no receipt line at all.
func checkReceiptGrowth(ctx context.Context, client sigs_client.Client, ns, controllerNS string) (r doctorResult, shown bool) {
	r = doctorResult{label: "Receipt storage"}
	var list v1alpha1.PipelineList
	scope := "all namespaces"
	if err := client.List(ctx, &list); err != nil {
		// Not allowed cluster-wide: the current namespace.
		scope = "namespace " + ns
		if err := client.List(ctx, &list, sigs_client.InNamespace(ns)); err != nil {
			r.icon, r.warned = doctorWarn, true
			r.detail = fmt.Sprintf("could not list Pipelines: %v", err)
			return r, true
		}
	}
	var count, bytes int64
	for _, p := range list.Items {
		for _, e := range p.Status.Environments {
			if e.Receipts != nil {
				count += e.Receipts.Count
				bytes += e.Receipts.Bytes
			}
		}
	}
	retention, known := receiptRetention(ctx, client, controllerNS)
	r.icon = doctorPass
	if count == 0 {
		r.detail = "no promotion receipts (" + scope + ")"
		return r, known
	}
	r.detail = fmt.Sprintf("%d receipts, %s (%s; retention %s)", count, humanBytes(bytes), scope, retention)
	if bytes > receiptGrowthWarnBytes && known && retention == "off" {
		r.icon, r.warned = doctorWarn, true
		r.hint = "every receipt is kept and they share etcd's quota: set receipts.retention.maxAge (docs/receipts.md, Size)"
	}
	return r, true
}

// receiptRetention is the controller's --receipt-retention-max-age, "off"
// when it is unset or not above 0, and whether the controller's Deployment
// could be read. The flags are read as the controller's flag set reads
// them: --flag=value or --flag value, the last one set wins, the duration
// parsed (0h and 0m are off; a value that does not parse is off too: the
// warning is the safe side).
func receiptRetention(ctx context.Context, client sigs_client.Client, controllerNS string) (string, bool) {
	var deps appsv1.DeploymentList
	if err := client.List(ctx, &deps, sigs_client.InNamespace(controllerNS)); err != nil {
		return "unknown", false
	}
	for _, d := range deps.Items {
		for _, c := range d.Spec.Template.Spec.Containers {
			receipts := false
			retention := "off"
			for i := 0; i < len(c.Args); i++ {
				name, value, hasValue := strings.Cut(c.Args[i], "=")
				switch name {
				case "--receipts":
					on, err := strconv.ParseBool(value)
					receipts = !hasValue || (err == nil && on)
				case "--receipt-retention-max-age":
					if !hasValue && i+1 < len(c.Args) {
						i++
						value = c.Args[i]
					}
					retention = "off"
					if age, err := time.ParseDuration(value); err == nil && age > 0 {
						retention = value
					}
				}
			}
			if receipts {
				return retention, true
			}
		}
	}
	return "unknown", false
}

// humanBytes is n in KiB, MiB or GiB.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
