//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// guideChartV0122 is the chart reference of the "Upgrading from v0.12.2"
// guide: the test replaces it with the checkout's chart. The release pin
// replaces <version> in the guide and here with the release's version.
const guideChartV0122 = "oci://ghcr.io/pnz1990/charts/kardinal-promoter --version <version>"

// TestUpgradeV0122_Estate is TestUpgradeV0120_Estate from kardinal-promoter
// v0.12.2 as released (its chart, digest-pinned image, CLI and kro;
// components/kardinal-v0122.sh): it builds the same estate under v0.12.2,
// with the automatic rollback in flight (u012AutoRollback), follows
// "Upgrading from v0.12.2" in docs/upgrade.md while versions are in flight,
// and makes the same checks.
//
// Covers UPG-V0122-01, UPG-V0122-02, UPG-V0122-03, UPG-V0122-04, UPG-V0122-05, UPG-V0122-06.
func TestUpgradeV0122_Estate(t *testing.T) {
	u012Upgrade(t, u012From{pins: "V0122", cli: (*framework.Env).KardinalV0122, guideChart: guideChartV0122,
		extra: u012AutoRollback})
}
