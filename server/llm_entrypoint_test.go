package server

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestLLMEntryPointsOnly guards the most expensive mistake in this area: a call
// site that grabs a bare provider and therefore silently loses the failover
// chain the operator configured. That is exactly how a pinned worker ended up
// retrying one drained MiniMax key until the run died while four healthy
// profiles sat idle.
//
// rawProviderForProfile is reachable only from the allow-list below. Anything
// that issues a model request must go through llmForProfile, llmForActive or
// globalProvider, which apply the chain according to the current switches.
func TestLLMEntryPointsOnly(t *testing.T) {
	// Each entry states why that helper legitimately needs the unwrapped
	// provider. Adding a name here is a deliberate statement that the function
	// never issues a model request (or owns its own failover bookkeeping).
	allowed := map[string]string{
		"llmForProfile":             "the wrapper itself",
		"llmForActive":              "the wrapper itself",
		"rawProviderForProfile":     "cache refill recursion",
		"applyLLM":                  "wraps the result with poolForActive itself",
		"poolMember":                "pool members must not recurse into a pool",
		"current":                   "the task chain owns its own profile advance in streamTaskLLM",
		"activeCfg":                 "reads config values only",
		"CompactionWindow":          "reads config values only",
		"workerForTaskSlot":         "reads config values only",
		"resolutionFromProfile":     "availability check, never issues a request",
		"buildApprovalSideSnapshot": "reads config values only",
		"taskRuntimeAvailable":      "availability check, never issues a request",
	}

	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// Name only: method sets here are unambiguous, and a bare name keeps
			// the allow-list readable when a helper moves between receivers.
			name := fn.Name.Name
			if _, ok := allowed[name]; ok {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "rawProviderForProfile" {
					return true
				}
				pos := fset.Position(call.Pos())
				offenders = append(offenders, fmt.Sprintf(
					"%s:%d in %s — use llmForProfile / llmForActive / globalProvider",
					filepath.Base(pos.Filename), pos.Line, name))
				return true
			})
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("bare LLM provider reaches a request path, so failover is silently disabled:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
