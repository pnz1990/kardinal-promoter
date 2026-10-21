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
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setForTest sets *p to v until t ends: t.Cleanup puts back what was there,
// whether the test passes, fails or stops at a require. A package variable
// (globalOutput, errOut, buildClient, ...) a test changes and resets on a
// plain line stays changed when the test stops before that line, and every
// later test that reads it fails.
func setForTest[T any](t testing.TB, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// keepStyle puts back, when t ends, what running the root command sets for
// the whole package: sty, errOut and skewCheck (useStyle, before every command). A test
// that runs the root command (Execute) calls it, so a later test that calls
// a function directly does not write to this test's buffer.
func keepStyle(t testing.TB) {
	t.Helper()
	oldSty, oldErr, oldSkew := sty, errOut, skewCheck
	t.Cleanup(func() { sty, errOut, skewCheck = oldSty, oldErr, oldSkew })
}

// TestPackageVariablesAreRestored: every Test, Benchmark and Fuzz function
// that changes a package variable of this package puts it back when it
// ends, so one failing test cannot fail the ones after it. A change is an
// assignment to the variable (or to a field, element or pointee of it), ++
// or --, or running the root command (Execute sets errOut, sty and
// skewCheck). It is
// put back by setForTest or keepStyle, or by a cleanup (the Cleanup of a
// *testing.T, B, F or testing.TB, or the test's own defer) that assigns it
// the copy saved from it (prev := v, if prev still holds it when the
// function ends), its initializer, or the zero value when it has none; any
// other value is a change. A cleanup may be a function literal, a local
// bound to one, a function by name (whose other changes count), or what a
// helper returns (t.Cleanup(stub())), whose other changes count too. A
// helper's own defers put back only its own changes, when it returns, never
// the caller's. Names are resolved by type: a local of the same name is not
// the package's, and the functions followed are every function and method
// of the package, test files or not, called directly, through a local (f :=
// stub; f()) or passed by name (t.Run("s", sub), taken to be called). So
// are (#2339) a function-typed package variable's initial function (var
// set = func() {...}), a method value (f := v.m), every method of the
// package that an interface method call can run, and the method of the
// initializer's type behind a package variable of another package's
// interface (var w io.Writer = impl{}). A method that changes what its
// pointer receiver points to changes the variable it is called on
// (bx.put("a") changes bx). A function literal that the package's own code
// (not a test file) stores in a value or a field (cobra.Command{RunE: ...})
// is not run where it is built; in a test file one is taken to be called
// (a table's setup func, interceptor.Funcs), and so is a package function
// or method value a test file refers to at all (r := runner{f: w{}.set}).
// The functions a package variable's initializer stores (var handlers =
// map[string]func(){...}), not those it calls while the package
// initializes, are taken to be called by whatever reads the variable. &v
// of a package variable, anywhere, hands it out (#2367). TestMain is
// exempt: it sets up the whole run. Not seen: writes through a pointer
// parameter or a method of another package (other than Execute), a
// function-typed package variable's value after a test swaps it, and a
// function value the package's own code stores in a struct field or holds
// in a parameter.
func TestPackageVariablesAreRestored(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	srcs := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		srcs[f] = string(b)
	}
	leaks, err := restoreLeaks(srcs)
	require.NoError(t, err)
	assert.Empty(t, leaks, "put each back with setForTest(t, &v, value), keepStyle(t) (for Execute), or save it and restore it in t.Cleanup")
}

// TestParallelTestsChangeNoPackageVariable (#2194): a test that calls
// t.Parallel runs next to the other parallel tests, so it must not change a
// package variable even if it puts it back: another one would see the
// change. Found as TestPackageVariablesAreRestored finds changes (helpers
// to any depth, resolved by type; Execute; setForTest).
func TestParallelTestsChangeNoPackageVariable(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	srcs := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		srcs[f] = string(b)
	}
	changes, err := parallelChanges(srcs)
	require.NoError(t, err)
	assert.Empty(t, changes, "a parallel test may not change package variables: drop t.Parallel, or pass the value in")
}

// TestParallelChanges: the check on small packages: a parallel test that
// changes a package variable is named even when it puts it back (directly,
// through setForTest, keepStyle or a helper); a serial one, or a parallel
// one that only reads, is not. src, when set, is another file: of package
// cmd, or a test file of package cmd_test.
func TestParallelChanges(t *testing.T) {
	t.Parallel()
	const pkg = "package cmd\n\nvar x string\nvar errOut, sty, skewCheck any\n"
	const box = "type box struct{ v string; n int }\nfunc (b *box) put(s string) { b.v = s }\n"
	cases := []struct {
		name, test string
		changes    []string
		src        string
	}{
		{"setForTest", `func TestA(t *testing.T) { t.Parallel(); setForTest(t, &x, "a") }`, []string{"TestA changes x"}, ""},
		{"restored in a Cleanup", `func TestA(t *testing.T) { t.Parallel(); prev := x; t.Cleanup(func() { x = prev }); x = "a" }`,
			[]string{"TestA changes x"}, ""},
		{"a helper", `func stub(t *testing.T) { prev := x; t.Cleanup(func() { x = prev }); x = "a" }
func TestA(t *testing.T) { t.Parallel(); stub(t) }`, []string{"TestA changes x"}, ""},
		{"keepStyle", `func keepStyle(t *testing.T) { a, b, c := errOut, sty, skewCheck; t.Cleanup(func() { errOut, sty, skewCheck = a, b, c }) }
func TestA(t *testing.T) { t.Parallel(); keepStyle(t) }`, []string{"TestA changes errOut", "TestA changes skewCheck", "TestA changes sty"}, ""},
		{"a flag bound to it (#2194)", `func newCmd() { flags.StringVar(&x, "x", "", "") }
func TestA(t *testing.T) { t.Parallel(); newCmd() }`, []string{"TestA changes x"}, ""},
		{"its address handed out", `func TestA(t *testing.T) { t.Parallel(); _ = json.Unmarshal(nil, &x) }`, []string{"TestA changes x"}, ""},
		{"a signal to the test process", `func TestA(t *testing.T) { t.Parallel(); p, _ := os.FindProcess(os.Getpid()); _ = p.Signal(os.Interrupt) }`,
			[]string{"TestA signals the test process"}, ""},
		{"syscall.Kill", `func TestA(t *testing.T) { t.Parallel(); go func() { _ = syscall.Kill(os.Getpid(), syscall.SIGINT) }() }`,
			[]string{"TestA signals the test process"}, ""},
		{"serial", `func TestA(t *testing.T) { setForTest(t, &x, "a") }`, nil, ""},
		{"reads only", `func TestA(t *testing.T) { t.Parallel(); _ = x }`, nil, ""},
		// QA-3 on #2324 (#2339): what a call runs is resolved by type.
		{"a pointer-receiver method on a package variable", box + `var bx box
func TestA(t *testing.T) { t.Parallel(); bx.put("a") }`, []string{"TestA changes bx"}, ""},
		{"a pointer-receiver method that counts", `type counter struct{ n int }
func (c *counter) inc() { c.n++ }
var cnt counter
func TestA(t *testing.T) { t.Parallel(); cnt.inc() }`, []string{"TestA changes cnt"}, ""},
		{"a pointer-receiver method through another", box + `func (b *box) reset() { b.put("") }
var bx box
func TestA(t *testing.T) { t.Parallel(); bx.reset() }`, []string{"TestA changes bx"}, ""},
		{"a pointer-receiver method on a package pointer", box + `var pb = &box{}
func TestA(t *testing.T) { t.Parallel(); pb.put("a") }`, []string{"TestA changes pb"}, ""},
		{"a pointer-receiver method on a field of a package variable", box + `var st struct{ b box }
func TestA(t *testing.T) { t.Parallel(); st.b.put("a") }`, []string{"TestA changes st"}, ""},
		{"a pointer-receiver method on a local", box + `func TestA(t *testing.T) { t.Parallel(); var b box; b.put("a") }`, nil, ""},
		{"a pointer-receiver method that only reads", `type box struct{ v string }
func (b *box) get() string { return b.v }
var bx box
func TestA(t *testing.T) { t.Parallel(); _ = bx.get() }`, nil, ""},
		{"a value-receiver method changes a copy", `type box struct{ v string }
func (b box) put(s string) { b.v = s }
var bx box
func TestA(t *testing.T) { t.Parallel(); bx.put("a") }`, nil, ""},
		{"a function-typed package variable", `var set = func() { x = "a" }
func TestA(t *testing.T) { t.Parallel(); set() }`, []string{"TestA changes x"}, ""},
		{"a function-typed package variable bound to a function", `func stub() { x = "a" }
var set = stub
func TestA(t *testing.T) { t.Parallel(); set() }`, []string{"TestA changes x"}, ""},
		{"an interface method", `type writer interface{ Write(s string) }
type impl struct{}
func (impl) Write(s string) { x = s }
var w writer = impl{}
func TestA(t *testing.T) { t.Parallel(); w.Write("a") }`, []string{"TestA changes x"}, ""},
		{"an interface method on a local", `type writer interface{ Write(s string) }
type impl struct{}
func (impl) Write(s string) { x = s }
func TestA(t *testing.T) { t.Parallel(); var w writer = impl{}; w.Write("a") }`, []string{"TestA changes x"}, ""},
		{"an interface method with a pointer receiver", `type writer interface{ Write(s string) }
type buf struct{ s string }
func (b *buf) Write(s string) { b.s = s }
var w writer = &buf{}
func TestA(t *testing.T) { t.Parallel(); w.Write("a") }`, []string{"TestA changes w"}, ""},
		{"another package's interface", `type impl struct{}
func (impl) Write(p []byte) (int, error) { x = "a"; return 0, nil }
func TestA(t *testing.T) { t.Parallel(); _, _ = w.Write(nil) }`, []string{"TestA changes x"},
			"package cmd\n\nimport \"io\"\n\nvar w io.Writer = impl{}\n"},
		{"a function-typed package variable from package cmd_test", `var Set = func() { x = "a" }`, []string{"TestB changes x"},
			"package cmd_test\n\nimport (\n\t\"testing\"\n\n\t\"cmd\"\n)\n\nfunc TestB(t *testing.T) { t.Parallel(); cmd.Set() }\n"},
		{"a method value", `type h struct{}
func (h) set() { x = "a" }
func TestA(t *testing.T) { t.Parallel(); f := h{}.set; f() }`, []string{"TestA changes x"}, ""},
		{"a method value on a package variable", box + `var bx box
func TestA(t *testing.T) { t.Parallel(); f := bx.put; f("a") }`, []string{"TestA changes bx"}, ""},
		{"a method value passed", `type h struct{}
func (h) sub(t *testing.T) { x = "a" }
func TestA(t *testing.T) { t.Parallel(); t.Run("s", h{}.sub) }`, []string{"TestA changes x"}, ""},
		{"a method value in the package's own code", `func TestA(t *testing.T) { t.Parallel(); do() }`, []string{"TestA changes bx"},
			"package cmd\n\n" + box + "var bx box\nfunc do() { f := bx.put; f(\"a\") }\n"},
		{"a method value passed in the package's own code", `func TestA(t *testing.T) { t.Parallel(); do() }`, []string{"TestA changes x"},
			"package cmd\n\ntype h struct{}\nfunc (h) set() { x = \"a\" }\nfunc each(f func()) { f() }\nfunc do() { each(h{}.set) }\n"},
		// QA-3 on #2367: a function reached through data, and an address
		// held in a local.
		{"a method value stored in a struct field", `type runner struct{ f func() }
type w struct{}
func (w) set() { x = "a" }
func TestA(t *testing.T) { t.Parallel(); r := runner{f: w{}.set}; r.f() }`, []string{"TestA changes x"}, ""},
		{"a package function stored in a slice", `func stub() { x = "a" }
func TestA(t *testing.T) { t.Parallel(); fs := []func(){stub}; fs[0]() }`, []string{"TestA changes x"}, ""},
		{"a package struct holding a method value", `type runner struct{ f func() }
type w struct{}
func (w) set() { x = "a" }
var rn = runner{f: w{}.set}
func TestA(t *testing.T) { t.Parallel(); rn.f() }`, []string{"TestA changes x"}, ""},
		{"a package map of funcs", `var handlers = map[string]func(){"a": func() { x = "a" }}
func TestA(t *testing.T) { t.Parallel(); handlers["a"]() }`, []string{"TestA changes x"}, ""},
		{"a package map of functions, read by a helper", `func stub() { x = "a" }
func run(name string) { handlers[name]() }
func TestA(t *testing.T) { t.Parallel(); run("a") }`, []string{"TestA changes x"},
			"package cmd\n\nvar handlers = map[string]func(){\"a\": stub}\n"},
		{"a function variable an initializer builds", `func stub() { x = "a" }
func wrap(f func()) func() { return f }
var h = wrap(stub)
func TestA(t *testing.T) { t.Parallel(); h() }`, []string{"TestA changes x"}, ""},
		{"a package variable's address in a local", box + `var bx box
func TestA(t *testing.T) { t.Parallel(); p := &bx; p.put("a") }`, []string{"TestA changes bx"}, ""},
		{"a function an initializer calls while the package initializes", `func mk() int { x = "a"; return 1 }
var n = mk()
func TestA(t *testing.T) { t.Parallel(); _ = n }`, nil, ""},
		{"a function the package stores in a value is not run", `func TestA(t *testing.T) { t.Parallel(); _ = mk() }`, nil,
			"package cmd\n\ntype c struct{ f func() }\nfunc runX() { x = \"a\" }\nfunc mk() c { return c{f: runX} }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srcs := map[string]string{
				"x.go":      pkg,
				"x_test.go": "package cmd\n\nimport \"testing\"\n\nfunc setForTest[T any](t *testing.T, p *T, v T) {}\n\n" + tc.test + "\n",
			}
			if strings.HasPrefix(tc.src, "package cmd_test") {
				srcs["y_test.go"] = tc.src
			} else if tc.src != "" {
				srcs["y.go"] = tc.src
			}
			got, err := parallelChanges(srcs)
			require.NoError(t, err)
			assert.Equal(t, tc.changes, got)
		})
	}
}

// TestRestoreLeaks: the check on small packages, a leak and a safe form of
// each kind.
func TestRestoreLeaks(t *testing.T) {
	t.Parallel()
	const pkg = "package cmd\n\nvar x, y string\nvar out *int\n"
	cases := []struct {
		name, test string
		leaks      []string
		src        string // more of x.go
	}{
		{"reset on a plain line", `func TestA(t *testing.T) { x = "json"; check(); x = "" }`, []string{"TestA changes x"}, ""},
		{"a Cleanup", `func TestA(t *testing.T) { prev := x; t.Cleanup(func() { x = prev }); x = "json" }`, nil, ""},
		{"a defer", `func TestA(t *testing.T) { prev := x; defer func() { x = prev }(); x = "json" }`, nil, ""},
		{"a restore func", `func TestA(t *testing.T) { prev := x; restore := func() { x = prev }; t.Cleanup(restore); x = "a" }`, nil, ""},
		{"setForTest", `func TestA(t *testing.T) { setForTest(t, &x, "a") }`, nil, ""},
		{"a local value is not a saved one", `func TestA(t *testing.T) { v := "json"; x = v }`, []string{"TestA changes x"}, ""},
		{"saved from another variable", `func TestA(t *testing.T) { prev := y; x = "a"; x = prev }`, []string{"TestA changes x"}, ""},
		{"a Cleanup that only reads it", `func TestA(t *testing.T) { t.Cleanup(func() { t.Log(x) }); x = "a" }`, []string{"TestA changes x"}, ""},
		{"a local of the same name", `func TestA(t *testing.T) { x := "a"; x = "b"; _ = x }`, nil, ""},
		{"a parameter of the same name", `func TestA(t *testing.T) { f := func(x string) { x = "b"; _ = x }; f("") }`, nil, ""},
		{"a shadow does not hide the package's", `func TestA(t *testing.T) { x = "a"; { x := "b"; _ = x } }`, []string{"TestA changes x"}, ""},
		{"++, a pointee", `func TestA(t *testing.T) { *out = 1; y += "a" }`, []string{"TestA changes out", "TestA changes y"}, ""},
		{"a helper that registers its restore", `func stub(t *testing.T) { prev := x; t.Cleanup(func() { x = prev }); x = "a" }
func TestA(t *testing.T) { stub(t); x = "b" }`, nil, ""},
		{"a helper that returns its restore", `func stub() func() { prev := x; x = "a"; return func() { x = prev } }
func TestA(t *testing.T) { t.Cleanup(stub()) }`, nil, ""},
		{"a helper that leaks", `func stub() { x = "a" }
func TestA(t *testing.T) { stub() }`, []string{"TestA changes x"}, ""},
		{"running the root command", `func TestA(t *testing.T) { root := NewRootCmd(); _ = root.Execute() }`,
			[]string{"TestA changes errOut", "TestA changes skewCheck", "TestA changes sty"}, ""},
		{"keepStyle", `func keepStyle(t *testing.T) { a, b, c := errOut, sty, skewCheck; t.Cleanup(func() { errOut, sty, skewCheck = a, b, c }) }
func TestA(t *testing.T) { keepStyle(t); root := NewRootCmd(); _ = root.Execute() }`, nil, ""},
		{"TestMain", `func TestMain(m *testing.M) { x = "a"; m.Run() }`, nil, ""},
		// #1778: what the check did not see.
		{"a subtest by name", `func sub(t *testing.T) { x = "a" }
func TestA(t *testing.T) { t.Run("s", sub) }`, []string{"TestA changes x"}, ""},
		{"a subtest by name that restores", `func sub(t *testing.T) { setForTest(t, &x, "a") }
func TestA(t *testing.T) { t.Run("s", sub) }`, nil, ""},
		{"a cleanup by name", `func reset() { x = "" }
func TestA(t *testing.T) { t.Cleanup(reset); x = "a" }`, nil, ""},
		{"a returned restore that misses one", `func stub() func() { px := x; x = "a"; y = "b"; return func() { x = px } }
func TestA(t *testing.T) { t.Cleanup(stub()) }`, []string{"TestA changes y"}, ""},
		{"a deferred returned restore that misses one", `func stub() func() { px := x; x = "a"; y = "b"; return func() { x = px } }
func TestA(t *testing.T) { defer stub()() }`, []string{"TestA changes y"}, ""},
		{"a saved copy changed", `func TestA(t *testing.T) { prev := x; prev = "a"; x = prev }`, []string{"TestA changes x"}, ""},
		{"a saved copy changed, in a cleanup", `func TestA(t *testing.T) { prev := x; prev = "b"; t.Cleanup(func() { x = prev }); x = "a" }`,
			[]string{"TestA changes x"}, ""},
		{"a saved copy saved again", `func TestA(t *testing.T) { prev := y; prev = x; t.Cleanup(func() { x = prev }); x = "a" }`, nil, ""},
		{"a method helper", `type h struct{}
func (h) stub() { x = "a" }
func TestA(t *testing.T) { var v h; v.stub() }`, []string{"TestA changes x"}, ""},
		{"a function value", `func stub() { x = "a" }
func TestA(t *testing.T) { f := stub; f() }`, []string{"TestA changes x"}, ""},
		{"a helper in a non-test file", `func TestA(t *testing.T) { setX() }`, []string{"TestA changes x"},
			"func setX() { x = \"a\" }\n"},
		{"a Cleanup that is not testing's", `type r struct{}
func (r) Cleanup(f func()) {}
func TestA(t *testing.T) { var c r; prev := x; c.Cleanup(func() { x = prev }); x = "a" }`, []string{"TestA changes x"}, ""},
		{"a helper's testing.TB", `func stub(tb testing.TB) { prev := x; tb.Cleanup(func() { x = prev }); x = "a" }
func TestA(t *testing.T) { stub(t) }`, nil, ""},
		{"a cleanup that sets another value", `func TestA(t *testing.T) { t.Cleanup(func() { x = "zzz" }); x = "a" }`,
			[]string{"TestA changes x"}, ""},
		{"a cleanup back to the zero value", `func TestA(t *testing.T) { t.Cleanup(func() { x = "" }); x = "a" }`, nil, ""},
		{"a cleanup back to the initializer", `func TestA(t *testing.T) { t.Cleanup(func() { z = mk(1) }); z = nil }`, nil,
			"var z = mk(1)\nfunc mk(int) *int { return nil }\n"},
		{"a cleanup to another call", `func TestA(t *testing.T) { t.Cleanup(func() { z = mk(2) }); z = nil }`,
			[]string{"TestA changes z"}, "var z = mk(1)\nfunc mk(int) *int { return nil }\n"},
		// QA on #2186.
		{"a helper's defer does not put back the caller's change", `func stub() { prev := x; defer func() { x = prev }(); x = "a" }
func TestA(t *testing.T) { stub(); x = "b" }`, []string{"TestA changes x"}, ""},
		{"a helper that puts back with its defer", `func stub() { prev := x; defer func() { x = prev }(); x = "a" }
func TestA(t *testing.T) { stub() }`, nil, ""},
		{"a saved copy changed after the cleanup", `func TestA(t *testing.T) { prev := x; t.Cleanup(func() { x = prev }); prev = "b"; x = "a" }`,
			[]string{"TestA changes x"}, ""},
		{"a saved copy changed after the defer", `func TestA(t *testing.T) { prev := x; defer func() { x = prev }(); prev = "b"; x = "a" }`,
			[]string{"TestA changes x"}, ""},
		{"a deferred reset by name", `func reset() { x = "" }
func TestA(t *testing.T) { defer reset(); x = "a" }`, nil, ""},
		{"a cleanup by name with another change", `func reset() { x = ""; y = "zz" }
func TestA(t *testing.T) { t.Cleanup(reset); x = "a" }`, []string{"TestA changes y"}, ""},
		{"a deferred reset by name with another change", `func reset() { x = ""; y = "zz" }
func TestA(t *testing.T) { defer reset(); x = "a" }`, []string{"TestA changes y"}, ""},
		// QA on #2186: in a test file a stored function literal counts as
		// called (table-driven setup funcs, interceptor.Funcs); only the
		// package's own code (a cobra command tree) stores what something
		// else runs later.
		{"a table-driven setup func", `func TestA(t *testing.T) { cases := []struct{ setup func() }{{setup: func() { x = "a" }}}; for _, tc := range cases { tc.setup() } }`,
			[]string{"TestA changes x"}, ""},
		{"a map of funcs", `func TestA(t *testing.T) { m := map[string]func(){"a": func() { x = "a" }}; m["a"]() }`, []string{"TestA changes x"}, ""},
		{"a func in a struct value", `type c struct{ f func() }
func TestA(t *testing.T) { v := c{f: func() { x = "a" }}; v.f() }`, []string{"TestA changes x"}, ""},
		{"a func assigned to a field, in a test", `type c struct{ f func() }
func TestA(t *testing.T) { var v c; v.f = func() { x = "a" }; v.f() }`, []string{"TestA changes x"}, ""},
		{"a function the package stores in a value is not run", `func TestA(t *testing.T) { _ = mk() }`, nil,
			"type c struct{ f func() }\nfunc mk() c { return c{f: func() { x = \"a\" }} }\n"},
		{"a function the package assigns to a field is not run", `func TestA(t *testing.T) { _ = mk() }`, nil,
			"type c struct{ f func() }\nfunc mk() c { var v c; v.f = func() { x = \"a\" }; return v }\n"},
		{"Execute stands for what cobra runs", `func TestA(t *testing.T) { root := NewRootCmd(); _ = root.Execute() }`,
			[]string{"TestA changes errOut", "TestA changes skewCheck", "TestA changes sty"},
			"func NewRootCmd() *c { return &c{f: func() { y = \"a\" }} }\ntype c struct{ f func() }\nfunc (*c) Execute() error { return nil }\n"},
		{"a pointer-receiver method on a package variable (#2339)", `func TestA(t *testing.T) { bx.put("a") }`, []string{"TestA changes bx"},
			"type box struct{ v string }\nfunc (b *box) put(s string) { b.v = s }\nvar bx box\n"},
		{"a pointer-receiver method on a package variable, restored", `func TestA(t *testing.T) { prev := bx; t.Cleanup(func() { bx = prev }); bx.put("a") }`,
			nil, "type box struct{ v string }\nfunc (b *box) put(s string) { b.v = s }\nvar bx box\n"},
		{"a Benchmark", `func BenchmarkA(b *testing.B) { x = "a" }`, []string{"BenchmarkA changes x"}, ""},
		{"a Fuzz test that restores", `func FuzzA(f *testing.F) { prev := x; f.Cleanup(func() { x = prev }); x = "a" }`, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaks, err := restoreLeaks(map[string]string{
				"x.go":      pkg + "var errOut, sty, skewCheck any\n" + tc.src,
				"x_test.go": "package cmd\n\nimport \"testing\"\n\n" + tc.test + "\n",
			})
			require.NoError(t, err)
			var got []string
			for _, l := range leaks {
				got = append(got, l[strings.Index(l, ": ")+2:])
			}
			assert.Equal(t, tc.leaks, got)
		})
	}
}

// newRestoreCheck parses and type-checks the package in srcs and returns
// its Test, Benchmark and Fuzz functions.
func newRestoreCheck(srcs map[string]string) (*restoreCheck, []*ast.FuncDecl, error) {
	fset := token.NewFileSet()
	a := &restoreCheck{fset: fset, info: &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{},
		Types: map[ast.Expr]types.TypeAndValue{}},
		scopes: map[*types.Scope]bool{}, pkgs: map[*types.Package]bool{}, decls: map[types.Object]*ast.FuncDecl{},
		byName: map[string]*ast.FuncDecl{}, testing: map[types.Object]bool{}, inits: map[string]string{},
		initType: map[types.Object]types.Type{}, done: map[*ast.FuncDecl]*restoreSummary{}, inTest: map[*ast.FuncDecl]bool{},
		initFuncs: map[types.Object][]target{}}
	var tests []*ast.FuncDecl
	names := make([]string, 0, len(srcs))
	for f := range srcs {
		names = append(names, f)
	}
	slices.Sort(names)
	byPkg := map[string][]*ast.File{}
	var files []*ast.File
	testFile := map[*ast.File]bool{}
	for _, f := range names {
		file, err := parser.ParseFile(fset, f, srcs[f], parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", f, err)
		}
		byPkg[file.Name.Name] = append(byPkg[file.Name.Name], file)
		files = append(files, file)
		testFile[file] = strings.HasSuffix(f, "_test.go")
	}
	// Each package (cmd, and cmd_test) is type-checked for what its names
	// refer to; imports are empty stand-ins, so their errors are ignored.
	for name, files := range byPkg {
		conf := types.Config{Importer: emptyImporter{}, Error: func(error) {}}
		pkg, _ := conf.Check(name, fset, files, a.info)
		a.scopes[pkg.Scope()] = true
		a.pkgs[pkg] = true
	}
	var funcVars [][2]*ast.Ident // var set = stub
	var stored []storedInit      // var handlers = map[string]func(){...}
	for _, file := range files {
		for _, d := range file.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				if obj := a.info.Defs[d.Name]; obj != nil {
					a.decls[obj] = d
				}
				a.inTest[d] = testFile[file]
				if d.Recv == nil && file.Name.Name == "cmd" {
					a.byName[d.Name.Name] = d
				}
				if testFile[file] && d.Recv == nil && d.Name.Name != "TestMain" &&
					(strings.HasPrefix(d.Name.Name, "Test") || strings.HasPrefix(d.Name.Name, "Benchmark") || strings.HasPrefix(d.Name.Name, "Fuzz")) {
					tests = append(tests, d)
				}
			case *ast.GenDecl:
				// What a package variable starts as: its initializer, or
				// "" for the zero value.
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok && d.Tok == token.VAR {
						for i, n := range vs.Names {
							init := ""
							if len(vs.Values) == len(vs.Names) {
								init = types.ExprString(vs.Values[i])
								obj := a.info.Defs[n]
								a.initType[obj] = a.info.TypeOf(vs.Values[i])
								// A function-typed variable runs what it
								// starts as, unless a test swaps it.
								switch v := vs.Values[i].(type) {
								case *ast.FuncLit:
									fd := &ast.FuncDecl{Name: n, Type: v.Type, Body: v.Body}
									a.decls[obj] = fd
									a.inTest[fd] = testFile[file]
								case *ast.Ident:
									funcVars = append(funcVars, [2]*ast.Ident{n, v})
								default:
									stored = append(stored, storedInit{obj, vs.Values[i], testFile[file]})
								}
							}
							a.inits[n.Name] = init
						}
					}
				}
			}
		}
		// The *testing.T, B, F and testing.TB parameters: their Cleanup is
		// testing's.
		ast.Inspect(file, func(n ast.Node) bool {
			if ft, ok := n.(*ast.FuncType); ok && ft.Params != nil {
				for _, f := range ft.Params.List {
					switch types.ExprString(f.Type) {
					case "*testing.T", "*testing.B", "*testing.F", "testing.TB":
						for _, id := range f.Names {
							if obj := a.info.Defs[id]; obj != nil {
								a.testing[obj] = true
							}
						}
					}
				}
			}
			return true
		})
	}
	for _, fv := range funcVars {
		if d := a.decls[a.info.Uses[fv[1]]]; d != nil {
			a.decls[a.info.Defs[fv[0]]] = d
		}
	}
	for obj, d := range a.decls {
		if v, ok := obj.(*types.Var); ok && v.Pkg() != nil && v.Pkg().Name() == "cmd" {
			a.byName[v.Name()] = d // a function-typed package variable, for cmd.Set()
		}
	}
	for _, st := range stored {
		if ts := a.storedFuncs(st.value, st.inTest); len(ts) > 0 {
			a.initFuncs[st.obj] = ts
		}
	}
	return a, tests, nil
}

// storedInit is a package variable's initializer that is not a function.
type storedInit struct {
	obj    types.Object
	value  ast.Expr
	inTest bool
}

// storedFuncs are the functions a package variable's initializer stores
// (a map, slice or struct of funcs, or a function or method value passed
// along): every function literal and every package function or method
// value in it that is not called right there, while the package
// initializes.
func (a *restoreCheck) storedFuncs(value ast.Expr, inTest bool) []target {
	var out []target
	called := map[ast.Node]bool{}
	ast.Inspect(value, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			called[n.Fun] = true
		case *ast.FuncLit:
			if !called[n] {
				fd := &ast.FuncDecl{Name: ast.NewIdent("func"), Type: n.Type, Body: n.Body}
				a.inTest[fd] = inTest
				out = append(out, target{decl: fd})
			}
			return false
		case *ast.Ident, *ast.SelectorExpr:
			if !called[n] && a.funcRef(n.(ast.Expr)) {
				out = append(out, a.callees(n.(ast.Expr), nil)...)
			}
		}
		return true
	})
	return out
}

// funcRef reports whether e names a function or method of the package (not
// a variable that holds one): a function, a method value or expression, or
// cmd.X from package cmd_test.
func (a *restoreCheck) funcRef(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		_, ok := a.object(x).(*types.Func)
		return ok && a.decls[a.object(x)] != nil
	case *ast.SelectorExpr:
		if _, ok := a.info.Uses[x.Sel].(*types.Func); ok {
			return true
		}
		id, ok := x.X.(*ast.Ident)
		return ok && id.Name == "cmd" && (a.object(id) == nil || isPkgName(a.object(id))) && a.byName[x.Sel.Name] != nil
	}
	return false
}

// restoreLeaks are the changes to package variables that the Test, Benchmark
// and Fuzz functions of the package in srcs (file name to source) do not put
// back, each as "file:line:col: TestX changes v", sorted.
func restoreLeaks(srcs map[string]string) ([]string, error) {
	a, tests, err := newRestoreCheck(srcs)
	if err != nil {
		return nil, err
	}
	var leaks []string
	for _, fn := range tests {
		sum := a.summary(fn)
		for v, pos := range sum.writes {
			if !sum.restored[v] && !sum.deferred[v] {
				leaks = append(leaks, fmt.Sprintf("%s: %s changes %s", a.fset.Position(pos), fn.Name.Name, v))
			}
		}
	}
	slices.Sort(leaks)
	return leaks, nil
}

// parallelChanges are the package variables the Test functions that call
// t.Parallel change (restored or not), each as "TestX changes v", sorted.
func parallelChanges(srcs map[string]string) ([]string, error) {
	a, tests, err := newRestoreCheck(srcs)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, fn := range tests {
		parallel := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Parallel" && len(c.Args) == 0 {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "t" {
						parallel = true
					}
				}
			}
			return !parallel
		})
		if !parallel {
			continue
		}
		sum := a.summary(fn)
		vars := map[string]bool{}
		for v := range sum.writes {
			vars[v] = true
		}
		for v := range sum.binds {
			vars[v] = true
		}
		for _, put := range []map[string]bool{sum.restored, sum.deferred} {
			for v := range put {
				if v != "" {
					vars[v] = true
				}
			}
		}
		for v := range vars {
			out = append(out, fmt.Sprintf("%s changes %s", fn.Name.Name, v))
		}
		if signalsSelf(fn.Body) {
			// SIGINT or SIGTERM to the test process ends every watch and
			// wait that a parallel test runs (signal.NotifyContext, #2194).
			out = append(out, fmt.Sprintf("%s signals the test process", fn.Name.Name))
		}
	}
	slices.Sort(out)
	return out, nil
}

// signalsSelf reports whether body sends a signal: a Signal call (on
// os.FindProcess(os.Getpid())) or syscall.Kill, in the test or a function
// literal of it.
func signalsSelf(body ast.Node) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
			id, _ := sel.X.(*ast.Ident)
			found = found || sel.Sel.Name == "Signal" || (id != nil && id.Name == "syscall" && sel.Sel.Name == "Kill")
		}
		return !found
	})
	return found
}

// emptyImporter imports every package as an empty one: the check needs the
// package's own names only.
type emptyImporter struct{}

func (emptyImporter) Import(path string) (*types.Package, error) {
	p := types.NewPackage(path, filepath.Base(path))
	p.MarkComplete()
	return p, nil
}

// restoreCheck is TestPackageVariablesAreRestored's view of the package.
type restoreCheck struct {
	fset    *token.FileSet
	info    *types.Info                    // what each name refers to
	scopes  map[*types.Scope]bool          // the package scopes
	pkgs    map[*types.Package]bool        // the packages (cmd, cmd_test)
	decls   map[types.Object]*ast.FuncDecl // every function and method with a body, test files or not, and every function-typed package variable's initial function
	byName  map[string]*ast.FuncDecl       // package cmd's functions, for cmd.X from package cmd_test
	testing map[types.Object]bool          // the *testing.T, B, F and testing.TB parameters
	inits   map[string]string              // a package variable's initializer ("" for the zero value)
	// initType is the type of a package variable's initializer: the
	// concrete type behind an interface of another package, which the
	// empty stand-in imports leave unresolved.
	initType map[types.Object]types.Type
	inTest   map[*ast.FuncDecl]bool // the functions of the test files
	// initFuncs are the functions a package variable's initializer stores
	// (handlers = map[string]func(){...}): a function that reads the
	// variable is taken to call them.
	initFuncs map[types.Object][]target
	done      map[*ast.FuncDecl]*restoreSummary
}

// target is a function a call can run and, for a method, the variable it
// is called on (its root: v of v.f, v[i]): what a pointer receiver's method
// changes through the receiver (recvWrites) is a change of it.
type target struct {
	decl *ast.FuncDecl
	recv types.Object
}

// restoreSummary is what a function changes and puts back.
type restoreSummary struct {
	writes   map[string]token.Pos // changed, where first
	restored map[string]bool      // put back by a cleanup it registers (at the end of the test)
	deferred map[string]bool      // put back by its own defers (when it returns)
	returns  map[string]bool      // put back by a function it returns
	// binds are the package variables it hands out by address
	// (fs.StringVar(&globalOutput, ...)): whoever holds the pointer writes
	// them later, a flag binding with its default at once. Building the
	// root command binds every persistent flag. TestPackageVariablesAreRestored
	// does not count them (the next build resets them to their defaults); a
	// parallel test may not (two builds at once race, #2194).
	binds map[string]bool
	// recvWrites: a method with a pointer receiver changes what its
	// receiver points to (b.v = s, or a method of the receiver's that does).
	recvWrites bool
}

// pkgVar is the package variable id names, or "" (a local, a field, a
// parameter, or anything else).
func (a *restoreCheck) pkgVar(id *ast.Ident) string { return a.pkgVarOf(a.info.ObjectOf(id)) }

// pkgVarOf is the name of package variable obj, or "".
func (a *restoreCheck) pkgVarOf(obj types.Object) string {
	if v, ok := obj.(*types.Var); ok && !v.IsField() && a.scopes[v.Parent()] {
		return v.Name()
	}
	return ""
}

// object is what id names (nil when unknown).
func (a *restoreCheck) object(id *ast.Ident) types.Object { return a.info.ObjectOf(id) }

// rootVar is the package variable an assignment target changes: v, v.F,
// v[k], *v.
func (a *restoreCheck) rootVar(e ast.Expr) string {
	if id := rootIdent(e); id != nil {
		return a.pkgVar(id)
	}
	return ""
}

// rootIdent is the name an assignment target changes: v of v, v.F, v[k],
// *v; nil for anything else.
func rootIdent(e ast.Expr) *ast.Ident {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x
		case *ast.SelectorExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		default:
			return nil
		}
	}
}

// callees are the functions e names, resolved by type: a function or
// method of the package, a function-typed package variable (what it starts
// as), a local bound to one (aliases: f := stub, f := v.m), cmd.X from
// package cmd_test; a method value or method expression (v.m, T.m) as the
// method; an interface method as every method of the package's types that
// implement it; a method on a package variable of another package's
// interface as its initializer's type's.
func (a *restoreCheck) callees(e ast.Expr, aliases map[types.Object][]target) []target {
	switch x := e.(type) {
	case *ast.Ident:
		if d := a.decls[a.object(x)]; d != nil {
			return []target{{decl: d}}
		}
		return aliases[a.object(x)]
	case *ast.SelectorExpr:
		var recv types.Object
		if id := rootIdent(x.X); id != nil {
			if _, isVar := a.object(id).(*types.Var); isVar {
				recv = a.object(id)
			}
		}
		fn, _ := a.info.Uses[x.Sel].(*types.Func)
		if d := a.decls[fn]; d != nil {
			return []target{{decl: d, recv: recv}}
		}
		if fn != nil {
			if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil && types.IsInterface(sig.Recv().Type()) {
				return a.implementations(sig.Recv().Type().Underlying().(*types.Interface), fn.Name(), recv)
			}
		}
		if id, ok := x.X.(*ast.Ident); ok && fn == nil {
			if id.Name == "cmd" && (a.object(id) == nil || isPkgName(a.object(id))) {
				if d := a.byName[x.Sel.Name]; d != nil {
					return []target{{decl: d}}
				}
				return nil
			}
			if t := a.initType[a.object(id)]; t != nil {
				if m, _, _ := types.LookupFieldOrMethod(t, true, pkgOf(t), x.Sel.Name); m != nil {
					if f, ok := m.(*types.Func); ok && a.decls[f] != nil {
						return []target{{decl: a.decls[f], recv: recv}}
					}
				}
			}
		}
	case *ast.ParenExpr:
		return a.callees(x.X, aliases)
	}
	return nil
}

// implementations are method name of every named type of the package (T
// or *T) that implements iface, each called on recv.
func (a *restoreCheck) implementations(iface *types.Interface, name string, recv types.Object) []target {
	var out []target
	seen := map[*ast.FuncDecl]bool{}
	for _, obj := range a.info.Defs {
		tn, ok := obj.(*types.TypeName)
		if !ok || !a.pkgs[tn.Pkg()] || types.IsInterface(tn.Type()) {
			continue
		}
		for _, t := range []types.Type{tn.Type(), types.NewPointer(tn.Type())} {
			if !types.Implements(t, iface) {
				continue
			}
			m, _, _ := types.LookupFieldOrMethod(t, true, tn.Pkg(), name)
			if f, ok := m.(*types.Func); ok && a.decls[f] != nil && !seen[a.decls[f]] {
				seen[a.decls[f]] = true
				out = append(out, target{decl: a.decls[f], recv: recv})
			}
		}
	}
	slices.SortFunc(out, func(x, y target) int { return int(x.decl.Pos() - y.decl.Pos()) })
	return out
}

// pkgOf is the package that declares named type t (or *t), for looking up
// its unexported methods.
func pkgOf(t types.Type) *types.Package {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok {
		return n.Obj().Pkg()
	}
	return nil
}

// isPkgName reports whether obj is an imported package's name.
func isPkgName(obj types.Object) bool {
	_, ok := obj.(*types.PkgName)
	return ok
}

// isZero reports whether e is a zero-value literal.
func isZero(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.BasicLit:
		return x.Value == `""` || x.Value == "``" || x.Value == "0"
	case *ast.Ident:
		return x.Name == "nil" || x.Name == "false"
	}
	return false
}

// restores are the package variables the body of a cleanup n puts back: an
// assignment of the copy saved from it (saved), of its initializer, or of
// the zero value when it has none; setForTest. Any other value is a write.
func (a *restoreCheck) restores(n ast.Node, saved map[types.Object]string) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE || len(n.Lhs) != len(n.Rhs) {
				return true
			}
			for i, l := range n.Lhs {
				v := a.rootVar(l)
				if v == "" {
					continue
				}
				r := n.Rhs[i]
				init, known := a.inits[v]
				rid, isID := r.(*ast.Ident)
				switch {
				case isID && a.object(rid) != nil && saved[a.object(rid)] == v:
				case known && init != "" && types.ExprString(r) == init:
				case known && init == "" && isZero(r):
				default:
					continue
				}
				out[v] = true
			}
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "setForTest" && len(n.Args) > 1 {
				if u, ok := n.Args[1].(*ast.UnaryExpr); ok {
					if v := a.rootVar(u.X); v != "" {
						out[v] = true
					}
				}
			}
		}
		return true
	})
	return out
}

// summary is what function fn changes and puts back, the functions it calls
// included.
func (a *restoreCheck) summary(fn *ast.FuncDecl) *restoreSummary {
	if s, ok := a.done[fn]; ok {
		return s
	}
	s := &restoreSummary{writes: map[string]token.Pos{}, restored: map[string]bool{}, deferred: map[string]bool{},
		returns: map[string]bool{}, binds: map[string]bool{}}
	a.done[fn] = s                            // a recursive call sees what is known so far
	saved := map[types.Object]string{}        // prev := v
	locals := map[types.Object]*ast.FuncLit{} // restore := func() { ... }
	aliases := map[types.Object][]target{}    // f := stub, f := v.m
	// A method's pointer receiver: what it changes through it, its caller's
	// variable changes.
	var recvObj types.Object
	if fn.Recv != nil && len(fn.Recv.List) == 1 && len(fn.Recv.List[0].Names) == 1 {
		if o := a.info.Defs[fn.Recv.List[0].Names[0]]; o != nil {
			if _, ptr := o.Type().(*types.Pointer); ptr {
				recvObj = o
			}
		}
	}
	// A closure reads its saved copies when it runs, at the end: what it
	// puts back is judged against saved once the whole body is read.
	var pending []func()
	later := func(into map[string]bool, body ast.Node) {
		pending = append(pending, func() {
			for v := range a.restores(body, saved) {
				into[v] = true
			}
		})
	}
	write := func(v string, pos token.Pos) {
		if _, ok := s.writes[v]; !ok && v != "" {
			s.writes[v] = pos
		}
	}
	// change is a change of assignment target l: of its package variable,
	// or through the method's pointer receiver.
	change := func(l ast.Expr, pos token.Pos) {
		if id := rootIdent(l); id != nil && recvObj != nil && a.object(id) == recvObj {
			if _, isID := l.(*ast.Ident); !isID {
				s.recvWrites = true
			}
		}
		write(a.rootVar(l), pos)
	}
	// throughRecv is t's changes through its pointer receiver, made to the
	// variable t was called on: a package variable's, or this method's own
	// receiver's.
	throughRecv := func(t target, pos token.Pos) {
		if t.recv == nil || !a.summary(t.decl).recvWrites {
			return
		}
		if t.recv == recvObj {
			s.recvWrites = true
		}
		write(a.pkgVarOf(t.recv), pos)
	}
	merge := func(into, from map[string]bool) {
		for v := range from {
			into[v] = true
		}
	}
	// call is a call of helper h at pos: what it changes and does not put
	// back (with a cleanup, or its own defers) is changed here; what it
	// registers a cleanup for is put back at the end of the test. Its
	// defers have run when it returns: they put back nothing of the caller's.
	call := func(t target, pos token.Pos) {
		h := t.decl
		if h == fn {
			return
		}
		throughRecv(t, pos)
		hs := a.summary(h)
		for v := range hs.writes {
			if !hs.restored[v] && !hs.deferred[v] {
				write(v, pos)
			}
		}
		merge(s.restored, hs.restored)
		merge(s.binds, hs.binds)
	}
	// named is function d registered as a cleanup or deferred by name: what
	// it assigns as a restore (initializer or zero value) is put back into
	// into; its other changes are changes here.
	named := func(t target, into map[string]bool, pos token.Pos) {
		d := t.decl
		throughRecv(t, pos)
		rest := a.restores(d.Body, nil)
		merge(into, rest)
		hs := a.summary(d)
		for v := range hs.writes {
			if !rest[v] && !hs.restored[v] && !hs.deferred[v] {
				write(v, pos)
			}
		}
		merge(s.restored, hs.restored)
	}
	// cleanup is what cleanup function e puts back, into into: a function
	// literal, a local bound to one, a function by name, or a helper's call
	// that returns one (whose own changes are changes here).
	cleanup := func(e ast.Expr, into map[string]bool) {
		switch x := e.(type) {
		case *ast.FuncLit:
			later(into, x)
		case *ast.Ident, *ast.SelectorExpr:
			if id, ok := x.(*ast.Ident); ok && locals[a.object(id)] != nil {
				later(into, locals[a.object(id)])
				return
			}
			for _, t := range a.callees(x, aliases) {
				named(t, into, x.Pos())
			}
		case *ast.CallExpr:
			for _, t := range a.callees(x.Fun, aliases) {
				call(t, x.Pos())
				merge(into, a.summary(t.decl).returns)
			}
		}
	}
	// notRef are the names and selectors that are not a value read: an
	// assignment's target, an address's operand (handed out), a selector's
	// name, a key. (A call's function is one: h() reads h.)
	notRef := map[ast.Node]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			notRef[n.Sel] = true
		case *ast.UnaryExpr:
			if n.Op == token.AND {
				notRef[n.X] = true
			}
		case *ast.AssignStmt:
			for _, l := range n.Lhs {
				notRef[l] = true
			}
		case *ast.KeyValueExpr:
			notRef[n.Key] = true
		}
		return true
	})
	// ref is e used as a value. A package variable's stored functions
	// (initFuncs) are taken to be called by whatever reads it; in a test
	// file, so is a package function or method value referenced at all
	// (stored in a struct field, a map, a slice).
	ref := func(e ast.Expr) {
		if notRef[e] {
			return
		}
		if id, ok := e.(*ast.Ident); ok {
			for _, t := range a.initFuncs[a.object(id)] {
				call(t, e.Pos())
			}
		}
		if a.inTest[fn] && a.funcRef(e) {
			for _, t := range a.callees(e, aliases) {
				call(t, e.Pos())
			}
		}
	}
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			ref(n)
		case *ast.SelectorExpr:
			ref(n)
		case *ast.UnaryExpr:
			// &v anywhere hands v out: whoever holds the pointer writes it.
			if n.Op == token.AND {
				if v := a.rootVar(n.X); v != "" {
					s.binds[v] = true
				}
			}
		case *ast.CompositeLit:
			// The package's own code stores functions that something else
			// runs later (cobra.Command{RunE: func...}; Execute stands for
			// cobra's). In a test, a stored function is taken to be called
			// (a table's setup func, interceptor.Funcs).
			if a.inTest[fn] {
				return true
			}
			for _, e := range n.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if _, isLit := kv.Value.(*ast.FuncLit); isLit {
						continue
					}
				}
				ast.Inspect(e, visit)
			}
			return false
		case *ast.AssignStmt:
			// So is one it assigns to a field (cmd.RunE = func...).
			stored := false
			for i, l := range n.Lhs {
				if _, isSel := l.(*ast.SelectorExpr); isSel && len(n.Rhs) == len(n.Lhs) {
					if _, isLit := n.Rhs[i].(*ast.FuncLit); isLit {
						stored = true
					}
				}
			}
			if stored && !a.inTest[fn] {
				for i, l := range n.Lhs {
					if _, isLit := n.Rhs[i].(*ast.FuncLit); !isLit {
						ast.Inspect(n.Rhs[i], visit)
					}
					change(l, l.Pos())
				}
				return false
			}
			for i, l := range n.Lhs {
				var r ast.Expr
				if len(n.Rhs) == len(n.Lhs) {
					r = n.Rhs[i]
				}
				rid, _ := r.(*ast.Ident)
				if id, ok := l.(*ast.Ident); ok && a.object(id) != nil && a.pkgVar(id) == "" {
					// A local: what it holds now.
					obj := a.object(id)
					delete(saved, obj)
					delete(locals, obj)
					delete(aliases, obj)
					if rid != nil && a.pkgVar(rid) != "" {
						saved[obj] = a.pkgVar(rid)
					}
					if r != nil {
						if ts := a.callees(r, aliases); len(ts) > 0 {
							aliases[obj] = ts
						}
					}
					if lit, ok := r.(*ast.FuncLit); ok {
						locals[obj] = lit
					}
					continue
				}
				if n.Tok == token.DEFINE {
					continue
				}
				v := a.rootVar(l)
				if rid != nil && v != "" && a.object(rid) != nil && saved[a.object(rid)] == v {
					continue // putting back what was saved from it
				}
				change(l, l.Pos())
			}
		case *ast.IncDecStmt:
			change(n.X, n.Pos())
		case *ast.DeferStmt:
			if ts := a.callees(n.Call.Fun, aliases); len(ts) > 0 {
				for _, t := range ts {
					named(t, s.deferred, n.Pos()) // defer reset(): it runs when fn returns
				}
			} else {
				cleanup(n.Call.Fun, s.deferred)
			}
			return false
		case *ast.ReturnStmt:
			for _, r := range n.Results {
				switch x := r.(type) {
				case *ast.FuncLit:
					later(s.returns, x)
				case *ast.Ident:
					if lit := locals[a.object(x)]; lit != nil {
						later(s.returns, lit)
					}
				case *ast.CallExpr:
					for _, t := range a.callees(x.Fun, aliases) {
						merge(s.returns, a.summary(t.decl).returns)
					}
				}
			}
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && sel.Sel.Name == "Cleanup" && a.testing[a.object(x)] && len(n.Args) == 1 {
					cleanup(n.Args[0], s.restored)
					return false
				}
				switch sel.Sel.Name {
				case "Execute", "ExecuteContext", "ExecuteC", "ExecuteContextC":
					write("errOut", n.Pos()) // useStyle, before every command
					write("sty", n.Pos())
					write("skewCheck", n.Pos())
				}
			}
			if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "setForTest" && len(n.Args) > 1 {
				if u, ok := n.Args[1].(*ast.UnaryExpr); ok {
					s.restored[a.rootVar(u.X)] = true
				}
			} else {
				for _, t := range a.callees(n.Fun, aliases) {
					call(t, n.Pos())
				}
			}
			// A function passed by name (t.Run("s", sub)), or a method
			// value (t.Run("s", v.sub)), is taken to be called.
			for _, arg := range n.Args {
				switch arg.(type) {
				case *ast.Ident, *ast.SelectorExpr:
					for _, t := range a.callees(arg, aliases) {
						call(t, arg.Pos())
					}
				}
			}
		}
		return true
	}
	ast.Inspect(fn.Body, visit)
	for _, f := range pending {
		f()
	}
	return s
}
