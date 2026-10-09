// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook

import (
	"math/rand"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// maxRenderAlloc is what one render may allocate, whatever the template.
const maxRenderAlloc = 16 << 20

// tmplGen writes random templates from the grammar parseBodyTemplate allows.
type tmplGen struct {
	r *rand.Rand
	b strings.Builder
}

var (
	// $-rooted, so they also work inside a with, where dot is a string.
	genFields = []string{"$.Event", "$.Key", "$.Pipeline", "$.Bundle", "$.Environment", "$.Message", "$.Timestamp",
		"$.PRURL", "$.Hook", "$.Namespace"}
	// genFuncs take and return strings, so generated calls mostly render
	// instead of failing on a type error early.
	genFuncs = []string{"print", "println", "html", "js", "urlquery", "json", "lower", "upper"}
)

// expr is a string-valued expression.
func (g *tmplGen) expr(depth int) string {
	switch n := g.r.Intn(10); {
	case n < 3 || depth > 6:
		return genFields[g.r.Intn(len(genFields))]
	case n == 3:
		return strconv.Quote(strings.Repeat(`"<&x`, g.r.Intn(20)))
	case n == 4:
		return "(truncate " + strconv.Itoa(g.r.Intn(70000)) + " " + g.expr(depth+1) + ")"
	case n == 5:
		return "(" + g.expr(depth+1) + " | " + genFuncs[g.r.Intn(len(genFuncs))] + ")"
	case n == 6:
		f := []string{"lower", "upper", "json"}[g.r.Intn(3)]
		return "(" + f + " " + g.expr(depth+1) + ")"
	default:
		f := []string{"print", "println", "html", "js", "urlquery"}[g.r.Intn(5)]
		args := make([]string, 1+g.r.Intn(4))
		for i := range args {
			args[i] = g.expr(depth + 1)
		}
		return "(" + f + " " + strings.Join(args, " ") + ")"
	}
}

// action is a top-level expression: a string, or its length.
func (g *tmplGen) action() string {
	if g.r.Intn(4) == 0 {
		return "len " + g.expr(0)
	}
	return g.expr(0)
}

func (g *tmplGen) block(depth int, max int) {
	for g.b.Len() < max {
		switch n := g.r.Intn(10); {
		case n < 2:
			g.b.WriteString(strings.Repeat("t", g.r.Intn(50)))
		case n < 7 || depth > 3:
			g.b.WriteString("{{ " + g.action() + " }}")
		case n == 7:
			g.b.WriteString("{{ if " + g.expr(0) + " }}")
			g.block(depth+1, g.b.Len()+200)
			g.b.WriteString("{{ else }}")
			g.block(depth+1, g.b.Len()+200)
			g.b.WriteString("{{ end }}")
		default:
			g.b.WriteString("{{ with " + g.expr(0) + " }}")
			g.block(depth+1, g.b.Len()+200)
			g.b.WriteString("{{ end }}")
		}
		if depth > 0 {
			return
		}
	}
}

func generateTemplate(seed int64) string {
	g := &tmplGen{r: rand.New(rand.NewSource(seed))}
	g.block(0, 1+g.r.Intn(16<<10))
	s := g.b.String()
	if len(s) > 16<<10 {
		s = s[:16<<10] // may cut an action: then it does not parse, which is fine
	}
	return s
}

// fuzzData has every field at a size that makes escaping functions grow the
// most: quotes, angle brackets and ampersands.
func fuzzData() *TemplateData {
	big := strings.Repeat(`"<&'x`, 12<<10) // 60 KiB
	return &TemplateData{Event: "Bundle.Failed", Key: "Bundle.Failed/app", Pipeline: big[:4096], Bundle: "app-v1",
		Environment: "prod", Message: big, Timestamp: "2026-10-09T00:00:00Z", PRURL: big[:2048], Hook: "h", Namespace: "ns"}
}

// renderAlloc parses and renders body and returns the bytes the render
// allocated (0 when the body does not parse).
func renderAlloc(t testing.TB, body string, data *TemplateData) uint64 {
	tmpl, err := parseBodyTemplate(body)
	if err != nil {
		return 0
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, _ = renderTemplate(tmpl, data, "text/plain")
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestRenderTemplate_RandomTemplatesStayBounded renders random templates
// of up to 16 KiB from the allowed grammar over 60 KiB fields: no render
// allocates more than 16 MiB.
func TestRenderTemplate_RandomTemplatesStayBounded(t *testing.T) {
	for name, data := range map[string]*TemplateData{"60 KiB fields": fuzzData(), "small fields": smallData()} {
		t.Run(name, func(t *testing.T) { randomTemplatesStayBounded(t, data) })
	}
}

// smallData has ordinary field sizes, so most generated templates render.
func smallData() *TemplateData {
	return &TemplateData{Event: "Bundle.Failed", Key: "Bundle.Failed/app-v1", Pipeline: "app", Bundle: "app-v1",
		Environment: "prod", Message: `Bundle "app-v1" <is> Failed & more`, Timestamp: "2026-10-09T00:00:00Z",
		PRURL: "https://git.example/pr/1", Hook: "h", Namespace: "ns"}
}

func randomTemplatesStayBounded(t *testing.T, data *TemplateData) {
	n := 300
	if testing.Short() {
		n = 50
	}
	parsed := 0
	for seed := int64(1); seed <= int64(n); seed++ {
		body := generateTemplate(seed)
		if _, err := parseBodyTemplate(body); err == nil {
			parsed++
		}
		if a := renderAlloc(t, body, data); a > maxRenderAlloc {
			t.Fatalf("seed %d: a %d-byte template allocated %d bytes:\n%.500s", seed, len(body), a, body)
		}
	}
	if parsed < n/4 {
		t.Fatalf("only %d of %d generated templates parse: the generator no longer exercises rendering", parsed, n)
	}
}

// TestRenderTemplate_QARegressions are the templates QA used to allocate
// hundreds of MiB: printf with argument indexes and variable doubling. Both
// are refused at parse time.
func TestRenderTemplate_QARegressions(t *testing.T) {
	for name, body := range map[string]string{
		"printf argument indexes": `{{ printf "` + strings.Repeat("%[1]s", 3000) + `" .Message }}`,
		"printf in a pipeline":    `{{ .Message | printf "%[1]s%[1]s%[1]s%[1]s" }}`,
		"variable doubling":       `{{$a := "xxxxxxxxxxxxxxxx"}}` + strings.Repeat(`{{$a = print $a $a}}`, 24),
		"declaration in a chain":  `{{ print ($x := .).Message }}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseBodyTemplate(body)
			if err == nil {
				t.Fatal("parsed")
			}
			if a := renderAlloc(t, body, fuzzData()); a != 0 {
				t.Fatalf("rendered, %d bytes", a)
			}
		})
	}
}

// FuzzRenderTemplate is TestRenderTemplate_RandomTemplatesStayBounded for
// `go test -fuzz`: the seed picks the generated template.
func FuzzRenderTemplate(f *testing.F) {
	for _, s := range []int64{1, 7, 42, 1009} {
		f.Add(s)
	}
	data := fuzzData()
	small := smallData()
	f.Fuzz(func(t *testing.T, seed int64) {
		body := generateTemplate(seed)
		for _, d := range []*TemplateData{data, small} {
			if a := renderAlloc(t, body, d); a > maxRenderAlloc {
				t.Fatalf("seed %d: allocated %d bytes:\n%.500s", seed, a, body)
			}
		}
	})
}
