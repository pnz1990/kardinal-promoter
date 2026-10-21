//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// guideChartV0123 is the chart reference of the guide the upgrade-v0123
// suite follows, "Upgrading from v0.12.0", which the v0.12 patches share:
// the test replaces it with the checkout's chart.
const guideChartV0123 = "oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.13.0"

// TestUpgradeV0123_Estate is TestUpgradeV0120_Estate from kardinal-promoter
// v0.12.3 as released (its chart, digest-pinned image, CLI and kro;
// components/kardinal-v0123.sh), the latest v0.12 patch: the release gate's
// previous release, which users upgrade from. It builds the same estate
// under v0.12.3, with the automatic rollback in flight (u0121AutoRollback),
// follows "Upgrading from v0.12.0" while versions are in flight, and makes
// the same checks. v0.12.3 has the #2271 fix, so it has no stuck-version
// case.
//
// Covers UPG-V0123-01, UPG-V0123-02, UPG-V0123-03, UPG-V0123-04, UPG-V0123-05, UPG-V0123-06.
func TestUpgradeV0123_Estate(t *testing.T) {
	u012Upgrade(t, u012From{pins: "V0123", cli: (*framework.Env).KardinalV0123, guideChart: guideChartV0123,
		guide: "0.12.0", extra: u0121AutoRollback})
}
