// Copyright 2026 The kardinal-promoter Authors.
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

package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&argoCDSetImageStep{})
}

// argoCDSetImageStep patches an ArgoCD Application's spec.source.helm.valuesObject
// in-place via the Kubernetes API. This implements the Kargo-equivalent "argocd-update"
// promotion path: teams that store application config inside the ArgoCD Application
// (rather than a GitOps repo) can use this step without restructuring their setup.
//
// Configuration comes from the environment's update.argocd:
//   - application (required): name of the ArgoCD Application resource
//   - namespace: namespace of the Application (default: "argocd")
//   - imageKey: dot-path within valuesObject where the tag is written
//     (default: "image.tag")
//
// The step is idempotent: patching the same tag twice is a no-op with StepSuccess.
// No git operations are performed — the entire promotion is a single Kubernetes patch.
//
// Because nothing is reviewed, the step refuses to run for an environment
// with approval: pr-review (C05-steps-11), and for config Bundles, which it
// cannot apply.
type argoCDSetImageStep struct{}

// argoCDPRReviewRejected is the failure message for argocd + pr-review.
const argoCDPRReviewRejected = "argocd-set-image: update.strategy argocd patches the Application directly " +
	"and cannot honour approval: pr-review; use approval: auto with a PolicyGate, or a git-based strategy " +
	"(kustomize or helm) for a reviewed promotion"

func (s *argoCDSetImageStep) Name() string { return "argocd-set-image" }

func (s *argoCDSetImageStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	// A spec the strategy refuses is a permanent error: retrying cannot fix it.
	if state.Environment.Approval == "pr-review" {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: argoCDPRReviewRejected},
			parentsteps.Permanent(errors.New(argoCDPRReviewRejected))
	}
	if state.Bundle.Type == "config" {
		msg := "argocd-set-image: config Bundles are not supported by update.strategy argocd"
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: msg}, parentsteps.Permanent(errors.New(msg))
	}
	// O4: K8sClient is required.
	if state.K8sClient == nil {
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: "argocd-set-image: K8sClient is required but not set in StepState",
		}, fmt.Errorf("argocd-set-image: K8sClient is required")
	}

	var cfg v1alpha1.ArgoCDUpdateConfig
	if state.Environment.Update.ArgoCD != nil {
		cfg = *state.Environment.Update.ArgoCD
	}
	appName := cfg.Application

	// O5: Application name is required.
	if appName == "" {
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: "argocd-set-image: update.argocd.application is required",
		}, parentsteps.Permanent(errors.New("argocd-set-image: update.argocd.application is required"))
	}

	namespace := cfg.Namespace
	if namespace == "" {
		namespace = "argocd"
	}

	imageKey := cfg.ImageKey
	if imageKey == "" {
		imageKey = "image.tag"
	}

	// Determine the tag to set from the Bundle images.
	tag := ""
	for _, img := range state.Bundle.Images {
		if img.Tag != "" {
			tag = img.Tag
			break
		}
	}
	if tag == "" {
		return parentsteps.StepResult{
			Status:  parentsteps.StepSuccess,
			Message: "argocd-set-image: no image tag to set",
		}, nil
	}

	// Read the current Application to check idempotency.
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})

	if err := state.K8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app); err != nil {
		if k8serrors.IsNotFound(err) {
			// O6: not found → StepFailed with "not found" in message.
			return parentsteps.StepResult{
				Status:  parentsteps.StepFailed,
				Message: fmt.Sprintf("argocd-set-image: Application %s/%s not found", namespace, appName),
			}, fmt.Errorf("argocd-set-image: Application %s/%s not found", namespace, appName)
		}
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: fmt.Sprintf("argocd-set-image: get Application %s/%s: %v", namespace, appName, err),
		}, fmt.Errorf("argocd-set-image: get Application: %w", err)
	}

	// Check if the tag is already set (idempotency — O3).
	existing := getValuesObjectKey(app.Object, imageKey)
	if existing == tag {
		return parentsteps.StepResult{
			Status:  parentsteps.StepSuccess,
			Message: fmt.Sprintf("argocd-set-image: %s/%s already has %s=%s", namespace, appName, imageKey, tag),
			Outputs: map[string]string{
				"argocdApplication": appName,
				"argocdNamespace":   namespace,
				"imageKey":          imageKey,
				"imageTag":          tag,
			},
		}, nil
	}

	// Build the merge patch that sets spec.source.helm.valuesObject.<imageKey> = tag.
	patchData, err := buildValuesObjectPatch(imageKey, tag)
	if err != nil {
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: fmt.Sprintf("argocd-set-image: build patch: %v", err),
		}, fmt.Errorf("argocd-set-image: build patch: %w", err)
	}

	if err := state.K8sClient.Patch(ctx, app, client.RawPatch(types.MergePatchType, patchData)); err != nil {
		return parentsteps.StepResult{
			Status:  parentsteps.StepFailed,
			Message: fmt.Sprintf("argocd-set-image: patch Application %s/%s: %v", namespace, appName, err),
		}, fmt.Errorf("argocd-set-image: patch Application: %w", err)
	}

	return parentsteps.StepResult{
		Status: parentsteps.StepSuccess,
		Message: fmt.Sprintf("argocd-set-image: patched Application %s/%s — %s=%s",
			namespace, appName, imageKey, tag),
		Outputs: map[string]string{
			"argocdApplication": appName,
			"argocdNamespace":   namespace,
			"imageKey":          imageKey,
			"imageTag":          tag,
		},
	}, nil
}

// getValuesObjectKey reads a dot-separated key from
// spec.source.helm.valuesObject in the Application's unstructured object.
// Returns "" if any part of the path is missing.
func getValuesObjectKey(obj map[string]interface{}, imageKey string) string {
	// Navigate: spec → source → helm → valuesObject
	spec, ok := obj["spec"].(map[string]interface{})
	if !ok {
		return ""
	}
	source, ok := spec["source"].(map[string]interface{})
	if !ok {
		return ""
	}
	helm, ok := source["helm"].(map[string]interface{})
	if !ok {
		return ""
	}
	valuesObject, ok := helm["valuesObject"].(map[string]interface{})
	if !ok {
		return ""
	}

	keys := strings.Split(imageKey, ".")
	current := valuesObject
	for i, k := range keys {
		if i == len(keys)-1 {
			v, _ := current[k].(string)
			return v
		}
		next, ok := current[k].(map[string]interface{})
		if !ok {
			return ""
		}
		current = next
	}
	return ""
}

// buildValuesObjectPatch constructs a JSON merge patch that sets
// spec.source.helm.valuesObject.<imageKey> = tag.
// The imageKey is a dot-separated path (e.g. "image.tag").
func buildValuesObjectPatch(imageKey, tag string) ([]byte, error) {
	// Build the nested valuesObject map from the dot-path.
	keys := strings.Split(imageKey, ".")
	var buildNested func(keys []string, value interface{}) interface{}
	buildNested = func(keys []string, value interface{}) interface{} {
		if len(keys) == 0 {
			return value
		}
		return map[string]interface{}{
			keys[0]: buildNested(keys[1:], value),
		}
	}

	valuesObject := buildNested(keys, tag)

	patchData := map[string]interface{}{
		"spec": map[string]interface{}{
			"source": map[string]interface{}{
				"helm": map[string]interface{}{
					"valuesObject": valuesObject,
				},
			},
		},
	}

	return json.Marshal(patchData)
}
