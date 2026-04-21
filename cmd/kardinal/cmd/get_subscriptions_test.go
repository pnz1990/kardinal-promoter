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
	"bytes"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// makeTestSubscription constructs a minimal Subscription for testing.
func makeTestSubscription(name, ns, pipeline string, subType v1alpha1.SubscriptionType, phase, lastBundle string) v1alpha1.Subscription {
	return v1alpha1.Subscription{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         ns,
			CreationTimestamp: metav1.Time{Time: time.Now().Add(-10 * time.Minute)},
		},
		Spec: v1alpha1.SubscriptionSpec{
			Type:     subType,
			Pipeline: pipeline,
		},
		Status: v1alpha1.SubscriptionStatus{
			Phase:             phase,
			LastCheckedAt:     time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339),
			LastBundleCreated: lastBundle,
		},
	}
}

func TestFormatSubscriptionTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := FormatSubscriptionTable(&buf, nil, false)
	require.NoError(t, err)
	// empty list: only header
	out := buf.String()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "TYPE")
	assert.Contains(t, out, "PIPELINE")
	assert.Contains(t, out, "PHASE")
	assert.Contains(t, out, "LAST-CHECK")
	assert.Contains(t, out, "LAST-BUNDLE")
	assert.Contains(t, out, "AGE")
}

func TestFormatSubscriptionTable_rows(t *testing.T) {
	subs := []v1alpha1.Subscription{
		makeTestSubscription("oci-watcher", "default", "my-pipeline", v1alpha1.SubscriptionTypeImage, "Watching", "my-pipeline-bundle-001"),
		makeTestSubscription("git-watcher", "default", "my-pipeline", v1alpha1.SubscriptionTypeGit, "Error", ""),
	}
	var buf bytes.Buffer
	err := FormatSubscriptionTable(&buf, subs, false)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "oci-watcher")
	assert.Contains(t, out, "image")
	assert.Contains(t, out, "Watching")
	assert.Contains(t, out, "my-pipeline-bundle-001")
	assert.Contains(t, out, "git-watcher")
	assert.Contains(t, out, "git")
	assert.Contains(t, out, "Error")
	// last-bundle for git-watcher with empty status
	assert.Contains(t, out, "-")
}

func TestFormatSubscriptionTable_showNamespace(t *testing.T) {
	subs := []v1alpha1.Subscription{
		makeTestSubscription("watcher", "my-ns", "pipe", v1alpha1.SubscriptionTypeImage, "Watching", ""),
	}
	var buf bytes.Buffer
	err := FormatSubscriptionTable(&buf, subs, true)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "NAMESPACE")
	assert.Contains(t, out, "my-ns")
}

func TestFormatPipelineTableFull_subColumn(t *testing.T) {
	pipe := v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-pipeline",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
		},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
			},
		},
	}
	subs := []v1alpha1.Subscription{
		makeTestSubscription("watcher-1", "default", "my-pipeline", v1alpha1.SubscriptionTypeImage, "Watching", ""),
		makeTestSubscription("watcher-2", "default", "my-pipeline", v1alpha1.SubscriptionTypeImage, "Watching", ""),
		// Idle subscription — should not count
		makeTestSubscription("watcher-idle", "default", "my-pipeline", v1alpha1.SubscriptionTypeImage, "Idle", ""),
		// Different pipeline — should not appear in SUB count for my-pipeline
		makeTestSubscription("other-watcher", "default", "other-pipeline", v1alpha1.SubscriptionTypeImage, "Watching", ""),
	}
	var buf bytes.Buffer
	err := FormatPipelineTableFull(&buf, []v1alpha1.Pipeline{pipe}, nil, subs, false)
	require.NoError(t, err)
	out := buf.String()
	// Header must have SUB column
	assert.Contains(t, out, "SUB")
	// Row must show 2 (only Watching subscriptions for my-pipeline)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Equal(t, 2, len(lines), "expected header + 1 data row")
	assert.Contains(t, lines[1], "2")
}

func TestFormatPipelineTableFull_noSubsShowsZero(t *testing.T) {
	pipe := v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "lonely-pipeline",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Now().Add(-5 * time.Minute)},
		},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{{Name: "prod"}},
		},
	}
	var buf bytes.Buffer
	err := FormatPipelineTableFull(&buf, []v1alpha1.Pipeline{pipe}, nil, nil, false)
	require.NoError(t, err)
	out := buf.String()
	// With nil subs: no SUB column
	assert.NotContains(t, out, "SUB")
}

func TestFormatSubscriptionTable_noLastChecked(t *testing.T) {
	sub := v1alpha1.Subscription{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "bare",
			Namespace:         "default",
			CreationTimestamp: metav1.Time{Time: time.Now().Add(-30 * time.Second)},
		},
		Spec: v1alpha1.SubscriptionSpec{
			Type:     v1alpha1.SubscriptionTypeGit,
			Pipeline: "pipe",
		},
		Status: v1alpha1.SubscriptionStatus{},
	}
	var buf bytes.Buffer
	err := FormatSubscriptionTable(&buf, []v1alpha1.Subscription{sub}, false)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "bare")
	// last-check shows "-" when no LastCheckedAt is set
	// last-bundle shows "-" when no LastBundleCreated is set
	assert.Contains(t, out, "-")
	// Phase should show "Unknown" when empty
	assert.Contains(t, out, "Unknown")
}
