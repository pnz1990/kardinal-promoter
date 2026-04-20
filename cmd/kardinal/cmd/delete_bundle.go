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
	"fmt"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newDeleteCmd() *cobra.Command {
	del := &cobra.Command{
		Use:   "delete",
		Short: "Delete kardinal resources",
	}
	del.AddCommand(newDeleteBundleCmd())
	return del
}

func newDeleteBundleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "bundle <name>",
		Aliases: []string{"bundles"},
		Short:   "Delete a Bundle by name",
		Long: `Delete a Bundle by name.

Deleting a Bundle cancels any in-progress promotion for that Bundle.
Superseded Bundles are deleted automatically by the garbage collector;
use this command to explicitly remove a Bundle before that occurs.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("delete bundle: %w", err)
			}
			return deleteBundleFn(cmd.OutOrStdout(), c, ns, args[0])
		},
	}
	return cmd
}

// deleteBundleFn is the testable implementation of delete bundle.
func deleteBundleFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, name string) error {
	bundle := &v1alpha1.Bundle{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: ns}, bundle); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("bundle %q not found in namespace %s", name, ns)
		}
		return fmt.Errorf("delete bundle: get %s: %w", name, err)
	}

	if err := c.Delete(context.Background(), bundle); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("bundle %q not found in namespace %s", name, ns)
		}
		return fmt.Errorf("delete bundle %s: %w", name, err)
	}

	if _, err := fmt.Fprintf(w, "Bundle %s deleted\n", name); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}
