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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&configMergeStep{})
}

// configMergeStep applies a config or mixed Bundle's configRef commit to the
// environment directory.
//
// git-clone checks the configRef commit out into
// parentsteps.ConfigSourceDir(WorkDir), a directory next to the working tree.
// config-merge reads it from that same derived path, never from
// Outputs["configSourceDir"] (which is informational). It copies only
// the environment's own subtree (environments[].path, or
// environments/<name>) from that commit over the same path in the working
// tree. .git and everything outside the subtree are never copied, so the
// config repository cannot overwrite other environments or the repo root
// (C05-steps-03). Files deleted in the config commit are not deleted.
//
// Idempotent: copying the same files twice produces the same result.
type configMergeStep struct{}

func (s *configMergeStep) Name() string { return "config-merge" }

func (s *configMergeStep) Execute(_ context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if state.Bundle.ConfigRef == nil || state.Bundle.ConfigRef.CommitSHA == "" {
		return parentsteps.StepResult{
			Status:  parentsteps.StepSuccess,
			Message: "no config ref — nothing to merge",
		}, nil
	}
	// fail refuses the promotion for good (parentsteps.Permanent): an unsafe
	// environment path, or a config source that is missing or has no
	// directory for this environment, does not change on retry.
	fail := func(format string, args ...any) (parentsteps.StepResult, error) {
		msg := fmt.Sprintf(format, args...)
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: msg},
			parentsteps.Permanent(errors.New(msg))
	}
	// ioFail reports a file system error, which is retried unless the checkout
	// refused a path that escapes it.
	ioFail := func(what string, err error) (parentsteps.StepResult, error) {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("%s: %v", what, err)},
			permanentIfEscape(fmt.Errorf("%s: %w", what, err))
	}

	envRel, err := envSubdir(state)
	if err != nil {
		return fail("%v", err)
	}
	dst, err := openCheckout(state)
	if err != nil {
		return ioFail("open checkout", err)
	}
	defer func() { _ = dst.Close() }()

	// The source path is derived from the work dir, never read from
	// Outputs["configSourceDir"]: outputs are restored from PromotionStep
	// status, which is not trusted as a filesystem path.
	srcDir := parentsteps.ConfigSourceDir(state.WorkDir)
	src, err := os.OpenRoot(srcDir)
	if errors.Is(err, fs.ErrNotExist) {
		return fail("config source not checked out at %s: git-clone must run before config-merge to check out the configRef commit", srcDir)
	}
	if err != nil {
		return ioFail("open config source", err)
	}
	defer func() { _ = src.Close() }()

	fsRel := filepath.ToSlash(envRel)
	if info, statErr := src.Stat(envRel); statErr != nil || !info.IsDir() {
		return fail("config commit %s has no directory %s", shortSHA(state.Bundle.ConfigRef.CommitSHA), fsRel)
	}
	if err := dst.MkdirAll(envRel, 0o755); err != nil {
		return ioFail("mkdir "+fsRel, err)
	}

	var merged, skipped int
	walkErr := fs.WalkDir(src.FS(), fsRel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return dst.MkdirAll(filepath.FromSlash(p), 0o755)
		}
		if !d.Type().IsRegular() {
			skipped++ // symlinks and special files are never copied
			return nil
		}
		data, err := src.ReadFile(filepath.FromSlash(p))
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		if err := dst.WriteFile(filepath.FromSlash(p), data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", p, err)
		}
		merged++
		return nil
	})
	if walkErr != nil {
		return ioFail("copy "+path.Clean(fsRel), walkErr)
	}

	msg := fmt.Sprintf("merged %d files into %s from config commit %s", merged, fsRel, shortSHA(state.Bundle.ConfigRef.CommitSHA))
	if skipped > 0 {
		msg += fmt.Sprintf(" (skipped %d symlinks or special files)", skipped)
	}
	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: msg,
		Outputs: map[string]string{"mergedFiles": fmt.Sprintf("%d", merged)},
	}, nil
}

// shortSHA returns the first 8 characters of a commit SHA.
func shortSHA(sha string) string {
	return sha[:min(8, len(sha))]
}
