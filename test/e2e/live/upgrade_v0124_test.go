//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// guideChartV0124 is the chart reference of the guide the upgrade-v0124
// suite follows, "Upgrading from v0.12.0", which the v0.12 patches share:
// the test replaces it with the checkout's chart.
const guideChartV0124 = "oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.13.0"

// TestUpgradeV0124_Estate is TestUpgradeV0120_Estate from kardinal-promoter
// v0.12.4 as released (its chart, image and CLI pinned by digest, and its
// kro; components/kardinal-v0124.sh), the latest v0.12 patch: the release
// gate's previous release, which users upgrade from. It builds the same
// estate under v0.12.4, with the automatic rollback in flight
// (u0121AutoRollback), follows "Upgrading from v0.12.0" while versions are
// in flight, and makes the same checks. v0.12.4 has the #2271 fix, as
// v0.12.3 has, so it has no stuck-version case.
//
// Covers UPG-V0124-01, UPG-V0124-02, UPG-V0124-03, UPG-V0124-04, UPG-V0124-05, UPG-V0124-06.
func TestUpgradeV0124_Estate(t *testing.T) {
	u012Upgrade(t, u012From{pins: "V0124", cli: (*framework.Env).KardinalV0124, guideChart: guideChartV0124,
		guide: "0.12.0", extra: u0121AutoRollback})
}
