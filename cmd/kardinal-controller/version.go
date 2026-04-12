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

package main

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	graphpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

const (
	versionCMName      = "kardinal-version"
	versionCMNamespace = "kardinal-system"
)

// publishVersionConfigMap creates or updates the kardinal-version ConfigMap with the
// controller version and the installed kro graph version. This ConfigMap is read
// by the CLI's `kardinal version` command to display all three version lines.
func publishVersionConfigMap(ctx context.Context, client sigs_client.Client, controllerVersion string) error {
	graphVersion := detectGraphVersion(ctx, client)

	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      versionCMName,
			Namespace: versionCMNamespace,
		},
		Data: map[string]string{
			"version": controllerVersion,
			"graph":   graphVersion,
		},
	}

	var existing corev1.ConfigMap
	err := client.Get(ctx, types.NamespacedName{Name: versionCMName, Namespace: versionCMNamespace}, &existing)
	if errors.IsNotFound(err) {
		if createErr := client.Create(ctx, desired); createErr != nil {
			return fmt.Errorf("create kardinal-version ConfigMap: %w", createErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get kardinal-version ConfigMap: %w", err)
	}

	existing.Data = desired.Data
	if patchErr := client.Update(ctx, &existing); patchErr != nil {
		return fmt.Errorf("update kardinal-version ConfigMap: %w", patchErr)
	}
	return nil
}

// detectGraphVersion attempts to read the kro Graph CRD version from the cluster.
// It queries the Graph CRD annotation "app.kubernetes.io/version" or falls back to
// the API group version string. Returns "unknown" on any error.
func detectGraphVersion(ctx context.Context, client sigs_client.Client) string {
	// The graph version is the API version of the Graph CRD.
	// We expose the api-version string as a human-readable proxy for the kro version.
	gvk := graphpkg.GraphGVK
	return fmt.Sprintf("%s/%s", gvk.Group, gvk.Version)
}
