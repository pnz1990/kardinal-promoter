//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// guideChartV0121 is the chart reference of the "Upgrading from v0.12.1"
// guide: the test replaces it with the checkout's chart.
const guideChartV0121 = "oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.2"

// TestUpgradeV0121_Estate is TestUpgradeV0120_Estate from kardinal-promoter
// v0.12.1 as released (its chart, digest-pinned image, CLI and kro;
// components/kardinal-v0121.sh): it builds the same estate under v0.12.1,
// follows "Upgrading from v0.12.1" in docs/upgrade.md while versions are
// in flight, and makes the same checks. The upgrade is checked to have
// happened by the controller's image, new Pods, a new deployed helm
// revision and the guide's server-side apply owning every kardinal CRD,
// not by the chart version: until the release pin, the checkout's chart is
// still 0.12.1.
//
// Covers UPG-V0121-01, UPG-V0121-02, UPG-V0121-03, UPG-V0121-04, UPG-V0121-05.
func TestUpgradeV0121_Estate(t *testing.T) {
	u012Upgrade(t, u012From{pins: "V0121", cli: (*framework.Env).KardinalV0121, guideChart: guideChartV0121})
}
