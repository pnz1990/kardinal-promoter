// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/overrideLimits.ts — the longest gate override the UI offers: the
// controller's default cap (--gate-override-max-minutes, 24 hours). A longer
// cap configured on the controller is still enforced there.
export const maxOverrideMinutes = 24 * 60
