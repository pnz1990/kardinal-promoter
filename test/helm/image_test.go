// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const modulePath = "github.com/kardinal-promoter/kardinal-promoter"

// dockerStage is one FROM ... block of the Dockerfile, without comments.
type dockerStage struct {
	from         string
	instructions []string
}

// dockerStages splits a Dockerfile into stages. Continuation lines are joined.
func dockerStages(content string) []dockerStage {
	var stages []dockerStage
	var cur strings.Builder
	flush := func() {
		ins := strings.TrimSpace(cur.String())
		cur.Reset()
		if ins == "" {
			return
		}
		if strings.HasPrefix(strings.ToUpper(ins), "FROM ") {
			stages = append(stages, dockerStage{from: ins})
			return
		}
		if len(stages) > 0 {
			stages[len(stages)-1].instructions = append(stages[len(stages)-1].instructions, ins)
		}
	}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasSuffix(trimmed, `\`) {
			cur.WriteString(strings.TrimSpace(strings.TrimSuffix(trimmed, `\`)) + " ")
			continue
		}
		cur.WriteString(trimmed)
		flush()
	}
	flush()
	return stages
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	require.NoError(t, err)
	return string(data)
}

// TestDockerfileBuildsForTheTargetPlatform checks that a multi-arch build
// produces binaries for each target platform: build stages run on
// BUILDPLATFORM and compile or download for TARGETARCH, and the final stage
// runs nothing, so no stage depends on emulation or on a hardcoded arch.
func TestDockerfileBuildsForTheTargetPlatform(t *testing.T) {
	stages := dockerStages(readRepoFile(t, "Dockerfile"))
	require.GreaterOrEqual(t, len(stages), 2, "expected build stages and a runtime stage")

	build, final := stages[:len(stages)-1], stages[len(stages)-1]
	all := ""
	for _, s := range build {
		assert.Contains(t, s.from, "--platform=$BUILDPLATFORM", "build stage %q must run on the build platform", s.from)
		for _, ins := range s.instructions {
			all += ins + "\n"
		}
	}
	for _, arch := range []string{"GOARCH=amd64", "GOARCH=arm64", "linux_amd64", "linux_arm64"} {
		assert.NotContains(t, all, arch, "the build stages must not hardcode an architecture")
	}
	assert.Contains(t, all, "GOARCH=${TARGETARCH}", "the controller must be compiled for the target architecture")
	assert.Contains(t, all, "linux_${TARGETARCH}.tar.gz", "kustomize must be downloaded for the target architecture")
	assert.Contains(t, all, "sha256sum -c", "the kustomize download must be verified")
	assert.NotRegexp(t, `apk add[^\n]*\bgit\b`, all, ".dockerignore excludes .git, so git in the builder stamps nothing")

	assert.NotContains(t, final.from, "--platform", "the runtime stage must use the target platform's base image")
	assert.Contains(t, final.from, "alpine", "the runtime stage must use alpine")
	var user, entrypoint string
	var copies []string
	for _, ins := range final.instructions {
		fields := strings.Fields(ins)
		switch strings.ToUpper(fields[0]) {
		case "RUN":
			t.Errorf("the runtime stage must not RUN commands (they would need emulation for arm64): %s", ins)
		case "USER":
			user = fields[1]
		case "ENTRYPOINT":
			entrypoint = ins
		case "COPY":
			copies = append(copies, ins)
		}
	}
	assert.Equal(t, "65532:65532", user, "the runtime stage must run as the nonroot UID")
	assert.Contains(t, entrypoint, "/bin/kardinal-controller")
	joined := strings.Join(copies, "\n")
	assert.Contains(t, joined, "/usr/local/bin/kustomize", "the runtime stage must ship kustomize (the kustomize-build step runs it)")
	assert.Contains(t, joined, "/bin/kardinal-controller")
}

func TestDockerStagesSplitsStages(t *testing.T) {
	stages := dockerStages("# c\nFROM --platform=$BUILDPLATFORM a AS b\nRUN x \\\n  y\n# RUN z\nFROM c\nUSER 1\n")
	require.Len(t, stages, 2)
	assert.Equal(t, []string{"RUN x y"}, stages[0].instructions)
	assert.Equal(t, "FROM c", stages[1].from)
	assert.Equal(t, []string{"USER 1"}, stages[1].instructions)
}

var (
	shellAssign = regexp.MustCompile(`(?m)^\s*([A-Z_][A-Z0-9_]*)="([^"]*)"\s*$`)
	ldflagsArg  = regexp.MustCompile(`-ldflags="([^"]*)"`)
	xFlag       = regexp.MustCompile(`-X\s+([^=\s]+)=`)
	buildTarget = regexp.MustCompile(`\s(\./cmd/[\w./-]+?)/?\s*$`)
)

// ldflagsVar is one -X <package>.<name> in a go build command.
type ldflagsVar struct {
	where, dir, name string
}

// ldflagsVars returns the -X variables of every go build command in a shell
// script or Dockerfile. Shell variables assigned in the same text are
// expanded, and continuation lines are joined.
func ldflagsVars(where, text string) ([]ldflagsVar, error) {
	vars := map[string]string{}
	for _, m := range shellAssign.FindAllStringSubmatch(text, -1) {
		vars[m[1]] = m[2]
	}
	joined := strings.ReplaceAll(text, "\\\n", " ")
	var out []ldflagsVar
	n := 0
	for _, line := range strings.Split(joined, "\n") {
		if !strings.Contains(line, "go build") {
			continue
		}
		n++
		for name, val := range vars {
			line = strings.ReplaceAll(line, "${"+name+"}", val)
		}
		target := buildTarget.FindStringSubmatch(line)
		if target == nil {
			return nil, fmt.Errorf("%s: no ./cmd/... build target in %q", where, line)
		}
		for _, lf := range ldflagsArg.FindAllStringSubmatch(line, -1) {
			for _, x := range xFlag.FindAllStringSubmatch(lf[1], -1) {
				sym := x[1]
				dot := strings.LastIndex(sym, ".")
				if dot < 0 {
					return nil, fmt.Errorf("%s: -X %s has no package", where, sym)
				}
				pkg, name := sym[:dot], sym[dot+1:]
				dir := strings.TrimPrefix(target[1], "./")
				if pkg != "main" {
					if !strings.HasPrefix(pkg, modulePath+"/") {
						return nil, fmt.Errorf("%s: -X %s is outside this module", where, sym)
					}
					dir = strings.TrimPrefix(pkg, modulePath+"/")
				}
				out = append(out, ldflagsVar{where: fmt.Sprintf("%s go build #%d (-X %s)", where, n, sym), dir: dir, name: name})
			}
		}
	}
	return out, nil
}

// declaresStringVar reports whether the non-test Go files in dir declare a
// package-level string variable called name. -X silently does nothing for any
// other symbol.
func declaresStringVar(t *testing.T, dir, name string) bool {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(repoRoot(t), dir, "*.go"))
	require.NoError(t, err, dir)
	fset := token.NewFileSet()
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		require.NoError(t, err, p)
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if id.Name != name {
						continue
					}
					if ident, ok := vs.Type.(*ast.Ident); ok {
						return ident.Name == "string"
					}
					if vs.Type == nil && i < len(vs.Values) {
						lit, ok := vs.Values[i].(*ast.BasicLit)
						return ok && lit.Kind == token.STRING
					}
				}
			}
		}
	}
	return false
}

// TestBuildLdflagsNameExistingVariables checks that every -X in the release
// workflow and the Dockerfile names a string variable that exists, so the
// shipped binaries report the release version.
func TestBuildLdflagsNameExistingVariables(t *testing.T) {
	var all []ldflagsVar
	for _, f := range []string{".github/workflows/release.yml", "Dockerfile"} {
		vars, err := ldflagsVars(f, readRepoFile(t, f))
		require.NoError(t, err)
		all = append(all, vars...)
	}
	require.GreaterOrEqual(t, len(all), 8, "expected -X flags for the controller, the CLI and the image")
	for _, v := range all {
		assert.True(t, declaresStringVar(t, v.dir, v.name), "%s: %s declares no string variable %s", v.where, v.dir, v.name)
	}

	assert.Contains(t, readRepoFile(t, "Dockerfile"), "-X main.ControllerVersion=${VERSION}",
		"the image's controller must report the VERSION build argument")
	assert.Regexp(t, `build-args:\s*\|\s*\n\s*VERSION=\$\{\{ env.VERSION \}\}`, readRepoFile(t, ".github/workflows/release.yml"),
		"release.yml must pass VERSION to the image build")
}

func TestLdflagsCheckCatchesKnownMistakes(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   map[string]bool // variable -> exists
	}{
		{
			name:   "the old release flags name variables that do not exist",
			script: `GOOS=linux go build -ldflags="-X main.version=v1 -X main.commit=abc" -o x ./cmd/kardinal-controller/`,
			want:   map[string]bool{"version": false, "commit": false},
		},
		{
			name: "expanded shell variables and the CLI package path",
			script: "CLI_LDFLAGS=\"-X " + modulePath + "/cmd/kardinal/cmd.CLIVersion=v1\"\n" +
				"go build \\\n  -ldflags=\"${CLI_LDFLAGS}\" -o x ./cmd/kardinal/\n",
			want: map[string]bool{"CLIVersion": true},
		},
		{
			name:   "a function is not a variable",
			script: `go build -ldflags="-X main.main=v1" ./cmd/kardinal-controller`,
			want:   map[string]bool{"main": false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars, err := ldflagsVars("test", tt.script)
			require.NoError(t, err)
			got := map[string]bool{}
			for _, v := range vars {
				got[v.name] = declaresStringVar(t, v.dir, v.name)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
