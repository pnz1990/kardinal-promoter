// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	yaml "go.yaml.in/yaml/v3"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// The render runs in its own Job with memory, CPU and time limits; the checks
// here refuse what would reach out of the DRY checkout or blow up inside the
// render before those limits are hit.

var kustomizationNames = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

func isKustomizationFile(p string) bool {
	for _, n := range kustomizationNames {
		if path.Base(p) == n {
			return true
		}
	}
	return false
}

// remoteRef reports whether s names something outside the checkout: a URL
// (any "://"), an scp-style git address, a go-getter ref, or a forge host
// path that kustomize would fetch with git.
func remoteRef(s string) bool {
	s = strings.TrimSpace(s)
	return strings.Contains(s, "://") || strings.HasPrefix(s, "git@") || strings.Contains(s, "?ref=") ||
		strings.Contains(s, "//?") || strings.HasPrefix(s, "github.com/") || strings.HasPrefix(s, "gitlab.com/") ||
		strings.HasPrefix(s, "bitbucket.org/")
}

// kustomizationDataField reports whether the scalar at field (a path of
// mapping keys, "[]" for a list element) is data kustomize writes into the
// objects, never a file or URL it loads: generator literals, labels and
// annotations, and inline patches. Every other scalar of a kustomization is
// checked with remoteRef.
func kustomizationDataField(field []string, value string) bool {
	if len(field) == 0 {
		return false
	}
	switch field[0] {
	case "commonAnnotations", "commonLabels", "metadata":
		return true
	case "labels":
		// labels[].pairs.<key>
		return len(field) >= 3 && field[2] == "pairs"
	case "configMapGenerator", "secretGenerator":
		// <gen>[].literals[]: key=value data. files and envs are loaded.
		return len(field) >= 3 && field[2] == "literals"
	case "patches", "patchesJson6902":
		// <patches>[].patch is the patch itself; .path is loaded.
		return len(field) >= 3 && field[2] == "patch"
	case "generators", "transformers", "validators":
		// An inline configuration is checked by checkPluginConfigs, field
		// by field.
		return strings.Contains(value, "\n")
	case "patchesStrategicMerge":
		// An entry is a file path or an inline patch (a YAML document,
		// always several lines).
		return strings.Contains(value, "\n")
	}
	return false
}

// checkKustomizations walks every scalar of every kustomization of the
// in-memory DRY tree and refuses one that names something outside the
// checkout (remoteRef), whatever the field: resources, components, bases,
// configMapGenerator files and envs, patch paths, openapi, crds and any
// other. Only data fields (kustomizationDataField) may hold a URL. helmCharts
// (chart inflation runs the helm binary) is refused too.
func checkKustomizations(mem filesys.FileSystem) error {
	return mem.Walk("/", func(p string, info fs.FileInfo, err error) error {
		if err != nil || info.IsDir() || !isKustomizationFile(p) {
			return err
		}
		raw, err := mem.ReadFile(p)
		if err != nil {
			return err
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return parentsteps.Permanent(fmt.Errorf("parse %s: %w", p, err))
		}
		if err := walkKustomization(p, &doc, nil, kustomizationDataField); err != nil {
			return err
		}
		return checkPluginConfigs(mem, p, &doc)
	})
}

// pluginFields are the kustomization fields whose entries are generator,
// transformer or validator configurations: a file of the DRY source, or the
// configuration inline. Their own fields (a ConfigMapGenerator's files, a
// PatchTransformer's path) are loaded too, so they are checked like a
// kustomization's.
var pluginFields = []string{"generators", "transformers", "validators"}

// pluginDataField reports whether a scalar of a plugin configuration is
// data: its metadata, generator literals and an inline patch.
func pluginDataField(field []string, _ string) bool {
	if len(field) == 0 {
		return false
	}
	switch field[0] {
	case "metadata", "literals", "patch":
		return true
	}
	return false
}

// checkPluginConfigs checks every generator, transformer and validator
// configuration a kustomization names, in a file or inline, for remote
// references (the configMapGenerator SSRF through a generator file).
func checkPluginConfigs(mem filesys.FileSystem, kustomization string, doc *yaml.Node) error {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	m := doc.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !slices.Contains(pluginFields, m.Content[i].Value) || m.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		for _, e := range m.Content[i+1].Content {
			if e.Kind != yaml.ScalarNode {
				continue
			}
			name, raw := kustomization+": "+m.Content[i].Value+" (inline)", []byte(e.Value)
			if !strings.Contains(e.Value, "\n") {
				p := filepath.Join(path.Dir(kustomization), e.Value)
				if mem.IsDir(p) {
					continue // a directory is a kustomization, checked on its own
				}
				b, err := mem.ReadFile(p)
				if err != nil {
					continue // kustomize reports the missing file
				}
				name, raw = p, b
			}
			dec := yaml.NewDecoder(strings.NewReader(string(raw)))
			for {
				var cfg yaml.Node
				err := dec.Decode(&cfg)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return parentsteps.Permanent(fmt.Errorf("parse %s: %w", name, err))
				}
				if cfg.Kind == yaml.DocumentNode && len(cfg.Content) == 1 && cfg.Content[0].Kind == yaml.MappingNode &&
					scalarValue(cfg.Content[0], "kind") == "HelmChartInflationGenerator" {
					return parentsteps.Permanent(fmt.Errorf("%s: HelmChartInflationGenerator is not supported by layout: branch; "+
						"put the chart at the environment path instead", name))
				}
				if err := walkKustomization(name, &cfg, nil, pluginDataField); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func walkKustomization(file string, n *yaml.Node, field []string, data func([]string, string) bool) error {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			if err := walkKustomization(file, c, field, data); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			if len(field) == 0 && data != nil && (key == "helmCharts" || key == "helmGlobals" || key == "helmChartInflationGenerator") {
				return parentsteps.Permanent(fmt.Errorf("%s: %s is not supported by layout: branch; "+
					"put the chart at the environment path instead", file, key))
			}
			if err := walkKustomization(file, n.Content[i+1], append(append([]string(nil), field...), key), data); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if err := walkKustomization(file, c, append(append([]string(nil), field...), "[]"), data); err != nil {
				return err
			}
		}
	case yaml.AliasNode:
		return parentsteps.Permanent(fmt.Errorf("%s: YAML aliases are not supported by layout: branch", file))
	case yaml.ScalarNode:
		if !data(field, n.Value) && remoteRef(n.Value) {
			return parentsteps.Permanent(fmt.Errorf("%s: %s: remote reference %q is not supported by layout: branch; "+
				"vendor it into the repository", file, fieldPath(field), n.Value))
		}
	}
	return nil
}

// fieldPath writes a field path as resources[] or configMapGenerator[].files[].
func fieldPath(field []string) string {
	var b strings.Builder
	for _, f := range field {
		if f != "[]" && b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(f)
	}
	return b.String()
}

// estimateKustomizeObjects returns a lower bound of how many objects
// kustomize build of dir produces: the documents of its resource files, its
// generators, and the estimate of every resource or component directory, each
// counted as often as it is included. Shared bases are counted once per path
// to them, as kustomize builds them, so an exponential diamond of overlays is
// seen without building it. It saturates above maxRenderedObjects.
func estimateKustomizeObjects(mem filesys.FileSystem, dir string) (int, error) {
	memo := map[string]int{}
	return estimateDir(mem, dir, memo, map[string]bool{})
}

func estimateDir(mem filesys.FileSystem, dir string, memo map[string]int, visiting map[string]bool) (int, error) {
	if n, ok := memo[dir]; ok {
		return n, nil
	}
	if visiting[dir] {
		return 0, parentsteps.Permanent(fmt.Errorf("kustomization %s includes itself", dir))
	}
	visiting[dir] = true
	defer delete(visiting, dir)
	var raw []byte
	for _, n := range kustomizationNames {
		if b, err := mem.ReadFile(filepath.Join(dir, n)); err == nil {
			raw = b
			break
		}
	}
	if raw == nil {
		return 0, nil // kustomize reports the missing kustomization itself
	}
	var k struct {
		Resources          []string    `yaml:"resources"`
		Bases              []string    `yaml:"bases"`
		Components         []string    `yaml:"components"`
		ConfigMapGenerator []yaml.Node `yaml:"configMapGenerator"`
		SecretGenerator    []yaml.Node `yaml:"secretGenerator"`
	}
	if err := yaml.Unmarshal(raw, &k); err != nil {
		return 0, parentsteps.Permanent(fmt.Errorf("parse %s: %w", dir, err))
	}
	total := len(k.ConfigMapGenerator) + len(k.SecretGenerator)
	add := func(n int) {
		total += n
		if total > maxRenderedObjects {
			total = maxRenderedObjects + 1
		}
	}
	for _, list := range [][]string{k.Resources, k.Bases, k.Components} {
		for _, r := range list {
			p := filepath.Join(dir, r)
			if mem.IsDir(p) {
				n, err := estimateDir(mem, p, memo, visiting)
				if err != nil {
					return 0, err
				}
				add(n)
				continue
			}
			b, err := mem.ReadFile(p)
			if err != nil {
				continue // kustomize reports it
			}
			add(countDocs(b))
		}
		if total > maxRenderedObjects {
			break
		}
	}
	memo[dir] = total
	return total, nil
}

// countDocs counts the non-empty YAML documents of a file.
func countDocs(b []byte) int {
	n := 0
	for _, d := range strings.Split("\n"+string(b), "\n---") {
		if t := strings.TrimSpace(d); t != "" && strings.Trim(t, "-") != "" {
			n++
		}
	}
	return n
}

// nondeterministicFuncs are the Helm template functions whose result changes
// from one render to the next (random values, the clock, generated keys and
// certificates, salted hashes). A chart that calls one renders different
// manifests for the same DRY commit.
var nondeterministicFuncs = []string{
	"randAlphaNum", "randAlpha", "randAscii", "randNumeric", "randBytes", "randInt", "shuffle",
	"uuidv4", "now", "ago",
	"genPrivateKey", "genCA", "genCAWithKey", "genSelfSignedCert", "genSelfSignedCertWithKey",
	"genSignedCert", "genSignedCertWithKey", "encryptAES", "bcrypt", "htpasswd", "getHostByName",
}

// helmOwnFuncs are the sprig names Helm replaces with its own functions
// (helm.sh/helm/v4/pkg/engine funcMap); they are not wrapped, so Helm's
// behaviour is kept.
var helmOwnFuncs = map[string]bool{
	"toToml": true, "mustToToml": true, "fromToml": true, "toYaml": true, "mustToYaml": true, "toYamlPretty": true,
	"fromYaml": true, "fromYamlArray": true, "toJson": true, "mustToJson": true, "fromJson": true, "fromJsonArray": true,
	"mustToDuration": true, "durationSeconds": true, "durationMilliseconds": true, "durationMicroseconds": true,
	"durationNanoseconds": true, "durationMinutes": true, "durationHours": true, "durationDays": true,
	"durationWeeks": true, "durationRoundTo": true, "durationTruncateTo": true,
	"include": true, "tpl": true, "required": true, "lookup": true,
}

// maxTemplateItems bounds a list or map a template function returns.
const maxTemplateItems = 100000

// templateFuncs returns the Helm template functions kardinal overrides:
//   - every sprig function and print, printf and println fail when their
//     result is a string over maxRenderOutputBytes or a list or map over
//     maxTemplateItems, and the functions that build big values from small
//     inputs (repeat, indent, nindent, replace, until, untilStep, seq) are
//     checked before they run, so a template that doubles a string in a loop
//     stops at the limit instead of filling the memory;
//   - the nondeterministic functions fail unless allowed.
func templateFuncs(allowNondeterministic bool) template.FuncMap {
	out := template.FuncMap{}
	for name, f := range sprig.TxtFuncMap() {
		if helmOwnFuncs[name] {
			continue // Helm's own version stays
		}
		out[name] = limitFunc(name, f)
	}
	// Helm removes these sprig functions: a template must not read the
	// render Job's environment.
	for _, name := range []string{"env", "expandenv"} {
		fn := name
		out[fn] = func(...interface{}) (string, error) {
			return "", fmt.Errorf("%s is not available in Helm templates", fn)
		}
	}
	out["print"] = limitFunc("print", fmt.Sprint)
	out["printf"] = limitFunc("printf", fmt.Sprintf)
	out["println"] = limitFunc("println", fmt.Sprintln)
	out["repeat"] = func(count int, s string) (string, error) {
		if count < 0 || int64(count)*int64(len(s)) > maxRenderOutputBytes {
			return "", outputLimitError("repeat")
		}
		return strings.Repeat(s, count), nil
	}
	indent := func(name string, newline bool) func(int, string) (string, error) {
		return func(spaces int, v string) (string, error) {
			lines := int64(strings.Count(v, "\n") + 1)
			if spaces < 0 || int64(len(v))+lines*int64(spaces) > maxRenderOutputBytes {
				return "", outputLimitError(name)
			}
			pad := strings.Repeat(" ", spaces)
			r := pad + strings.ReplaceAll(v, "\n", "\n"+pad)
			if newline {
				r = "\n" + r
			}
			return r, nil
		}
	}
	out["indent"] = indent("indent", false)
	out["nindent"] = indent("nindent", true)
	out["replace"] = func(old, new, src string) (string, error) {
		n := int64(strings.Count(src, old))
		if old == "" {
			n = int64(len(src)) + 1
		}
		if int64(len(src))+n*int64(len(new)) > maxRenderOutputBytes {
			return "", outputLimitError("replace")
		}
		return strings.ReplaceAll(src, old, new), nil
	}
	sprigUntil := sprig.TxtFuncMap()["until"].(func(int) []int)
	out["until"] = func(n int) ([]int, error) {
		if n > maxTemplateItems || n < -maxTemplateItems {
			return nil, outputLimitError("until")
		}
		return sprigUntil(n), nil
	}
	sprigUntilStep := sprig.TxtFuncMap()["untilStep"].(func(int, int, int) []int)
	out["untilStep"] = func(start, stop, step int) ([]int, error) {
		if step == 0 || (int64(stop)-int64(start))/int64(step) > maxTemplateItems {
			return nil, outputLimitError("untilStep")
		}
		return sprigUntilStep(start, stop, step), nil
	}
	sprigSeq := sprig.TxtFuncMap()["seq"].(func(...int) string)
	out["seq"] = func(params ...int) (string, error) {
		for _, p := range params {
			if p > maxTemplateItems || p < -maxTemplateItems {
				return "", outputLimitError("seq")
			}
		}
		return sprigSeq(params...), nil
	}
	if !allowNondeterministic {
		for _, name := range nondeterministicFuncs {
			fn := name
			out[fn] = func(...interface{}) (string, error) {
				return "", fmt.Errorf("%s changes from one render to the next; layout: branch refuses it unless "+
					"render.allowNondeterministic is set", fn)
			}
		}
	}
	return out
}

func outputLimitError(fn string) error {
	return fmt.Errorf("%s: the result would be larger than the render limit (%d MiB or %d items)",
		fn, maxRenderOutputBytes>>20, maxTemplateItems)
}

// limitFunc wraps template function f so that a result over the limits
// fails the template (text/template turns the panic into an error).
func limitFunc(name string, f interface{}) interface{} {
	v := reflect.ValueOf(f)
	t := v.Type()
	if t.Kind() != reflect.Func || t.NumOut() == 0 {
		return f
	}
	return reflect.MakeFunc(t, func(args []reflect.Value) []reflect.Value {
		var out []reflect.Value
		if t.IsVariadic() {
			out = v.CallSlice(args)
		} else {
			out = v.Call(args)
		}
		if tooBig(out[0]) {
			panic(outputLimitError(name))
		}
		return out
	}).Interface()
}

func tooBig(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return v.Len() > maxRenderOutputBytes
	case reflect.Slice, reflect.Array, reflect.Map:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			return v.Len() > maxRenderOutputBytes
		}
		return v.Len() > maxTemplateItems
	case reflect.Interface:
		if v.IsNil() {
			return false
		}
		return tooBig(v.Elem())
	}
	return false
}
