// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"strings"

	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
)

// receiptKey is a receipt's Bundle and environment.
type receiptKey struct{ bundle, env string }

// pipelineReceipts are the PromotionReceipts of pipeline, by Bundle and
// environment. A cluster without receipts (no CRD, no read access) has
// none: history and why show nothing about receipts then.
func pipelineReceipts(ctx context.Context, c sigs_client.Client, ns, pipeline string) map[receiptKey]v1alpha1.PromotionReceipt {
	var list v1alpha1.PromotionReceiptList
	if err := c.List(ctx, &list, sigs_client.InNamespace(ns), sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline}); err != nil {
		return nil
	}
	out := map[receiptKey]v1alpha1.PromotionReceipt{}
	for _, rc := range list.Items {
		out[receiptKey{rc.Labels["kardinal.io/bundle"], rc.Labels["kardinal.io/environment"]}] = rc
	}
	return out
}

// receiptSigned reports whether rc is signed (phase Signed or Stored).
func receiptSigned(rc v1alpha1.PromotionReceipt) bool {
	return rc.Status.Phase == v1alpha1.ReceiptSigned || rc.Status.Phase == v1alpha1.ReceiptStored
}

// receiptMark is a receipt's state for a person: "signed ✓" once the
// controller signed it (the mark on a terminal only), else its phase.
// "Verified" is what kardinal verify-receipt says of a receipt it checked,
// not the controller's signature (#2135).
func receiptMark(ctx context.Context, rc v1alpha1.PromotionReceipt) string {
	env := envOf(ctx)
	if receiptSigned(rc) {
		return env.sty.Paint(display.ToneVerified, "signed"+env.sty.Sym(" ✓", ""))
	}
	if rc.Status.Phase == "" {
		return "pending"
	}
	return strings.ToLower(rc.Status.Phase)
}
