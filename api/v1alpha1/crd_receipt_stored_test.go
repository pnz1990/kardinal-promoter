// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestReceiptStoredHoldsEveryImage (#2331): a PromotionReceipt's
// status.stored has room for every sink of a Bundle's receipt: the CR, the
// transparency log and an OCI referrer for each of the Bundle's images
// (spec.images, at most 100). At 16 the API server refused the status write
// after the referrers of 15 images or more, every time: the receipt never
// reached Stored. One entry more than that is refused.
//
// Covers RECEIPT-OCI-04.
func TestReceiptStoredHoldsEveryImage(t *testing.T) {
	images := int64(crdSchema(t, "kardinal.io_bundles.yaml", "spec", "images")["maxItems"].(float64))
	stored := int64(crdSchema(t, "kardinal.io_promotionreceipts.yaml", "status", "stored")["maxItems"].(float64))
	assert.Equal(t, images+2, stored, "status.stored: the CR, Rekor, and a referrer for each of spec.images' %d images", images)

	crds := loadCRDs(t)
	receipt := func(oci int) map[string]interface{} {
		entries := []interface{}{
			map[string]interface{}{"sink": "crd", "at": "2026-10-20T00:00:00Z"},
			map[string]interface{}{"sink": "rekor", "logIndex": int64(7), "url": "https://rekor.example", "at": "2026-10-20T00:00:00Z"},
		}
		for i := range oci {
			entries = append(entries, map[string]interface{}{"sink": "oci", "subject": fmt.Sprintf("ghcr.io/acme/app-%03d", i),
				"ref": fmt.Sprintf("ghcr.io/acme/app-%03d@sha256:%064x", i, i), "at": "2026-10-20T00:00:00Z"})
		}
		return map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1", "kind": "PromotionReceipt",
			"metadata": map[string]interface{}{"name": "checkout-1-5-0-prod", "namespace": "payments"},
			"spec":     map[string]interface{}{"statement": "{}", "statementDigest": "sha256:" + strings.Repeat("a", 64)},
			"status":   map[string]interface{}{"phase": "Stored", "bundle": "{}", "stored": entries},
		}
	}
	assert.Empty(t, validateCR(t, crds, receipt(int(images))), "a receipt of %d images, stored in every sink, is valid", images)
	assert.NotEmpty(t, validateCR(t, crds, receipt(int(images)+1)), "one entry past the bound is refused")
}
