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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// envSubdir returns the environment's directory relative to the checkout
// root: environments[].path, or environments/<name> when it is unset.
//
// The path must stay inside the checkout: absolute paths and paths that climb
// out with ".." are rejected (C05-steps-13). Symlinks are handled by opening
// the checkout with openCheckout and doing all file IO through the os.Root.
func envSubdir(state *parentsteps.StepState) (string, error) {
	p := state.Environment.Path
	if p == "" {
		if state.Environment.Name == "" {
			return "", fmt.Errorf("environment has no name and no path")
		}
		p = filepath.Join("environments", state.Environment.Name)
	}
	rel, err := confinedRel(p)
	if err != nil {
		return "", fmt.Errorf("environment path: %w", err)
	}
	return rel, nil
}

// confinedRel cleans p and returns it when it is a relative path that stays
// inside its root.
func confinedRel(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is empty")
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return "", fmt.Errorf("path %q must be relative to the repository root", p)
	}
	c := filepath.Clean(filepath.FromSlash(p))
	if c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q must stay inside the repository", p)
	}
	return c, nil
}

// openCheckout opens the git working directory as an os.Root. Every read and
// write through it is confined to the checkout, including through symlinks,
// so a repository cannot make a step touch another promotion's files.
func openCheckout(state *parentsteps.StepState) (*os.Root, error) {
	if state.WorkDir == "" {
		return nil, fmt.Errorf("WorkDir not set")
	}
	root, err := os.OpenRoot(state.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("open work dir: %w", err)
	}
	return root, nil
}
