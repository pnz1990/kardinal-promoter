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

package v1alpha1_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// TestCRDPrinterColumnsDrift verifies that +kubebuilder:printcolumn annotations
// in api/v1alpha1/*_types.go are faithfully reflected in config/crd/bases/*.yaml.
//
// This prevents a silent drift where a developer adds a printcolumn annotation
// to a Go type without regenerating the CRD YAML via `make manifests`.
//
// Checks (per O1-O3 in spec.md):
//   - Every column declared in Go annotations appears in the CRD YAML.
//   - name, type, and jsonPath all match.
func TestCRDPrinterColumnsDrift(t *testing.T) {
	// ── locate repository root ──────────────────────────────────────────────
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	// thisFile = .../api/v1alpha1/crd_printer_columns_test.go
	apiDir := filepath.Dir(thisFile)
	repoRoot := filepath.Join(apiDir, "..", "..")
	crdDir := filepath.Join(repoRoot, "config", "crd", "bases")

	// ── parse printer column annotations from Go source ──────────────────
	// Pattern: // +kubebuilder:printcolumn:name="Phase",type=string,...
	printColRE := regexp.MustCompile(
		`\+kubebuilder:printcolumn:name="([^"]+)",type=([^,\n]+)(?:,format=[^,\n]+)?(?:,priority=\d+)?,JSONPath=` +
			"`([^`]+)`",
	)
	printColRE2 := regexp.MustCompile(
		`\+kubebuilder:printcolumn:name="([^"]+)",type="([^"]+)"(?:,format="[^"]+")?,JSONPath="([^"]+)"`,
	)

	// kindPrintCols maps "Kind" → []printColumn (from Go annotations).
	type printColumn struct {
		Name     string
		Type     string
		JSONPath string
	}
	kindAnnotations := map[string][]printColumn{}

	goFiles, err := filepath.Glob(filepath.Join(apiDir, "*_types.go"))
	require.NoError(t, err)
	require.NotEmpty(t, goFiles, "no *_types.go files found in %s", apiDir)

	// Track which kind a set of annotations belongs to.
	// Annotations appear just above the type declaration:
	//   // +kubebuilder:printcolumn:...
	//   // +kubebuilder:printcolumn:...
	//   type Bundle struct { ...
	kindRE := regexp.MustCompile(`^type (\w+) struct`)

	for _, goFile := range goFiles {
		data, err := os.ReadFile(goFile)
		require.NoError(t, err, "read %s", goFile)

		lines := strings.Split(string(data), "\n")
		var pendingCols []printColumn

		for _, line := range lines {
			trimmed := strings.TrimSpace(line)

			// Check for printcolumn annotation (quoted JSONPath with backtick or double-quote).
			var col *printColumn
			if m := printColRE.FindStringSubmatch(trimmed); m != nil {
				col = &printColumn{Name: m[1], Type: m[2], JSONPath: m[3]}
			} else if m := printColRE2.FindStringSubmatch(trimmed); m != nil {
				col = &printColumn{Name: m[1], Type: m[2], JSONPath: m[3]}
			}
			if col != nil {
				pendingCols = append(pendingCols, *col)
				continue
			}

			// If we hit a struct declaration, associate pending columns with this kind.
			if m := kindRE.FindStringSubmatch(trimmed); m != nil && len(pendingCols) > 0 {
				kind := m[1]
				kindAnnotations[kind] = append(kindAnnotations[kind], pendingCols...)
				pendingCols = nil
				continue
			}

			// If we hit a non-comment, non-empty, non-annotation line: reset pending.
			if trimmed != "" && !strings.HasPrefix(trimmed, "//") {
				pendingCols = nil
			}
		}
	}

	require.NotEmpty(t, kindAnnotations, "no +kubebuilder:printcolumn annotations found in api/v1alpha1/")

	// ── load CRD YAML files ───────────────────────────────────────────────
	type crdColumn struct {
		Name     string `yaml:"name" json:"name"`
		Type     string `yaml:"type" json:"type"`
		JSONPath string `yaml:"jsonPath" json:"jsonPath"`
	}
	type crdVersion struct {
		Name                  string      `yaml:"name" json:"name"`
		AdditionalPrinterCols []crdColumn `yaml:"additionalPrinterColumns" json:"additionalPrinterColumns"`
	}
	type crdSpec struct {
		Names    struct{ Kind string `yaml:"kind" json:"kind"` } `yaml:"names" json:"names"`
		Versions []crdVersion `yaml:"versions" json:"versions"`
	}
	type crdFile struct {
		Spec crdSpec `yaml:"spec" json:"spec"`
	}

	// Map kind → CRD columns from YAML files.
	crdCols := map[string][]crdColumn{}

	crdFiles, err := filepath.Glob(filepath.Join(crdDir, "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, crdFiles, "no CRD YAML files found in %s", crdDir)

	for _, cf := range crdFiles {
		data, err := os.ReadFile(cf)
		require.NoError(t, err, "read %s", cf)
		var crd crdFile
		if err := yaml.Unmarshal(data, &crd); err != nil {
			continue // skip non-CRD YAML
		}
		kind := crd.Spec.Names.Kind
		if kind == "" {
			continue
		}
		for _, v := range crd.Spec.Versions {
			crdCols[kind] = append(crdCols[kind], v.AdditionalPrinterCols...)
		}
	}

	// ── compare ───────────────────────────────────────────────────────────
	for kind, annotations := range kindAnnotations {
		yamlCols, ok := crdCols[kind]
		if !ok {
			// Kind not in CRD files — may be an embedded struct; skip.
			continue
		}

		// Index YAML columns by name for fast lookup.
		yamlByName := map[string]crdColumn{}
		for _, c := range yamlCols {
			yamlByName[c.Name] = c
		}

		for _, ann := range annotations {
			yamlCol, exists := yamlByName[ann.Name]
			assert.True(t, exists,
				"kind %s: printcolumn %q declared in Go annotation not found in CRD YAML\n"+
					"  Run 'make manifests' to regenerate config/crd/bases/ and commit the result",
				kind, ann.Name)
			if !exists {
				continue
			}

			assert.Equal(t, ann.Type, yamlCol.Type,
				fmt.Sprintf("kind %s: printcolumn %q type mismatch\n  Go annotation: %q\n  CRD YAML:      %q",
					kind, ann.Name, ann.Type, yamlCol.Type))

			// Normalize JSONPath for comparison: strip surrounding backticks or quotes.
			normAnn := strings.Trim(ann.JSONPath, "`\"")
			normYAML := strings.Trim(yamlCol.JSONPath, "`\"")
			assert.Equal(t, normAnn, normYAML,
				fmt.Sprintf("kind %s: printcolumn %q jsonPath mismatch\n  Go annotation: %q\n  CRD YAML:      %q",
					kind, ann.Name, normAnn, normYAML))
		}
	}
}
