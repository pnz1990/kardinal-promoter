// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden statements in testdata")

// exempted is input("image") for a step that went on through a freeze under
// an exemption.
func exempted() Input {
	in := input("image")
	until := metav1.NewTime(verifiedAt.Add(-30 * time.Minute))
	in.Step.Status.FreezeExemptions = []v1alpha1.StepFreezeExemption{{Freeze: "kardinal-freeze-everyone", Actors: "everyone",
		Exemption: v1alpha1.FreezeExemption{Namespace: "payments", Pipeline: "checkout", Environment: "prod",
			Reason: "hotfix INC-4212", CreatedBy: "carol@example.com",
			Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "carol@example.com"}, ExpiresAt: until},
		Until: until}}
	return in
}

// TestBuild_GoldenStatements (#2060): the statement of a promotion with
// and without a freeze exemption, byte for byte (go test -update rewrites
// testdata). The exempted one records the freeze, who made the exemption,
// why and until when, and claims KARDINAL_FREEZE_EXEMPTED; the other has
// neither.
//
// Covers RECEIPT-STATEMENT-04.
func TestBuild_GoldenStatements(t *testing.T) {
	for name, in := range map[string]Input{"statement": input("image"), "statement-freeze-exempted": exempted()} {
		t.Run(name, func(t *testing.T) {
			raw, _, err := Build(in)
			require.NoError(t, err)
			var pretty map[string]interface{}
			require.NoError(t, json.Unmarshal(raw, &pretty))
			got, err := json.MarshalIndent(pretty, "", "  ")
			require.NoError(t, err)
			path := filepath.Join("testdata", name+".json")
			if *updateGolden {
				require.NoError(t, os.MkdirAll("testdata", 0o755))
				require.NoError(t, os.WriteFile(path, append(got, '\n'), 0o644))
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, string(want), string(got)+"\n")
		})
	}
	plain, _, err := Build(input("image"))
	require.NoError(t, err)
	st, err := Parse(plain)
	require.NoError(t, err)
	assert.NotContains(t, st.Predicate.VerifiedLevels, LevelFreezeExempted)
	assert.Empty(t, st.Predicate.Kardinal.FreezeExemptions)
}

// TestVerify_RequireNotFreezeExempted: verify-receipt --require
// '!FREEZE_EXEMPTED' refuses a receipt of an exempted promotion and admits
// one without.
func TestVerify_RequireNotFreezeExempted(t *testing.T) {
	s, pub := keySigner(t)
	for _, tc := range []struct {
		in      Input
		refused bool
	}{{input("image"), false}, {exempted(), true}} {
		raw, _, err := Build(tc.in)
		require.NoError(t, err)
		signed, err := s.Sign(context.Background(), raw)
		require.NoError(t, err)
		_, err = Verify(signed.Bundle, VerifyOptions{Key: pub, Require: []string{"!FREEZE_EXEMPTED"}})
		if tc.refused {
			require.Error(t, err)
			assert.Contains(t, err.Error(), "KARDINAL_FREEZE_EXEMPTED")
		} else {
			require.NoError(t, err)
		}
	}
}
