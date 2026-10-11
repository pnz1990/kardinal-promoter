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

package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Every command that names a pipeline and an environment takes them the same
// way: kardinal <verb> <pipeline> [<env>]. The flags each command had before
// (--env, -e, --stage, --pipeline) keep working as aliases, and a positional
// that disagrees with its flag is an error rather than a silent choice.

// addEnvFlag registers --env (shorthand -e) on cmd with usage, and makes
// --stage an alias of it.
func addEnvFlag(cmd *cobra.Command, target *string, usage string) {
	cmd.Flags().StringVarP(target, "env", "e", "", usage)
	aliasFlag(cmd, "stage", "env")
}

// aliasFlag makes --old an alias of --name on cmd's flags: cobra parses
// --old as --name, and help lists --name only.
func aliasFlag(cmd *cobra.Command, old, name string) {
	prev := cmd.Flags().GetNormalizeFunc()
	cmd.Flags().SetNormalizeFunc(func(f *pflag.FlagSet, n string) pflag.NormalizedName {
		if n == old {
			n = name
		}
		return prev(f, n)
	})
}

// envArgs returns the pipeline (args[0]) and the environment of a
// <pipeline> [<env>] command: args[1], else the --env flag (flagEnv). Both
// given and different is an error.
func envArgs(args []string, flagEnv string) (pipeline, env string, err error) {
	if len(args) == 0 {
		return "", "", fmt.Errorf("a pipeline name is required")
	}
	pipeline, env = args[0], flagEnv
	if len(args) > 1 {
		if flagEnv != "" && flagEnv != args[1] {
			return "", "", fmt.Errorf("the environment is given twice: %q as an argument and %q with --env; give it once", args[1], flagEnv)
		}
		env = args[1]
	}
	return pipeline, env, nil
}

// requireEnv is envArgs for a command that needs the environment.
func requireEnv(cmdPath string, args []string, flagEnv string) (pipeline, env string, err error) {
	pipeline, env, err = envArgs(args, flagEnv)
	if err == nil && env == "" {
		err = fmt.Errorf("which environment? Give it after the pipeline: %s %s <env>", cmdPath, pipeline)
	}
	return pipeline, env, err
}

// pipelineArg returns the pipeline of a command that took it only as
// --pipeline before: the positional args[0], else flagPipeline. Both given
// and different is an error.
func pipelineArg(args []string, flagPipeline string) (string, error) {
	if len(args) == 0 {
		return flagPipeline, nil
	}
	if flagPipeline != "" && flagPipeline != args[0] {
		return "", fmt.Errorf("the pipeline is given twice: %q as an argument and %q with --pipeline; give it once", args[0], flagPipeline)
	}
	return args[0], nil
}

// parseDuration parses a positive duration with Go units (30m, 4h) or whole
// days (2d). Every duration flag of the CLI uses it.
func parseDuration(s string) (time.Duration, error) {
	var d time.Duration
	if days, ok := strings.CutSuffix(strings.TrimSpace(s), "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("want a whole number of days such as 2d, or 30m or 4h")
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(strings.TrimSpace(s)); err != nil {
			return 0, fmt.Errorf("want 30m, 4h or 2d")
		}
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return d, nil
}

// durationValue is a pflag.Value for a duration flag that also takes days
// (parseDuration). The zero value means "not set".
type durationValue struct{ d *time.Duration }

func (v durationValue) String() string {
	if v.d == nil || *v.d == 0 {
		return "0s"
	}
	return v.d.String()
}

func (v durationValue) Set(s string) error {
	d, err := parseDuration(s)
	if err != nil {
		return err
	}
	*v.d = d
	return nil
}

func (durationValue) Type() string { return "duration" }

// ReferenceAnnotation holds the detail of a command that its reference page
// (hack/gen-cli-docs) shows after Long, and --help leaves out, so --help
// stays short.
const ReferenceAnnotation = "kardinal.io/reference"

// withReference sets cmd's ReferenceAnnotation to ref.
func withReference(cmd *cobra.Command, ref string) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[ReferenceAnnotation] = ref
}
